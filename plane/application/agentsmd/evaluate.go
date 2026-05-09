package agentsmd

import (
	"context"
	"fmt"
	"path"
	"strings"
)

// ZeroOID is the all-zero SHA-1 OID Git uses to denote "no object" — i.e.
// the old side of a branch creation or the new side of a branch deletion.
const ZeroOID = "0000000000000000000000000000000000000000"

// RefUpdate mirrors the shape of plane/git/gittypes.RefUpdate. We
// duplicate the type here to keep agentsmd free of plane/git imports
// (plane boundary). The hook adapter package converts between the two.
type RefUpdate struct {
	RefName string
	OldOID  string
	NewOID  string
}

// EvaluationInput is the per-push payload passed to Evaluate. Files is
// invoked lazily by predicates that need a changed-file list; predicates
// that operate purely on ref-level inputs (force_push, delete, push_to)
// never call Files.
type EvaluationInput struct {
	RepoID  string
	AgentID string
	Updates []RefUpdate
	Files   FileResolver
}

// FileResolver supplies the file-level information predicates need. The
// production implementation in plane/git/gitaly wraps Gitaly RPCs; tests
// inject in-memory implementations. All methods accept a context for
// cancellation; implementations must not block indefinitely.
type FileResolver interface {
	// Changed returns the list of repository-relative paths whose blob
	// changed between oldOID and newOID. For a branch creation
	// (oldOID == ZeroOID) implementations should return all paths reachable
	// from newOID.
	Changed(ctx context.Context, oldOID, newOID string) ([]string, error)
	// Size returns the size in bytes of the blob at the given path under
	// the given commit OID.
	Size(ctx context.Context, oid string, path string) (int64, error)
	// IsFastForward reports whether newOID is a strict descendant (or
	// equal) of oldOID. Used by force_push detection.
	IsFastForward(ctx context.Context, oldOID, newOID string) (bool, error)
}

// Violation is a single matched Never predicate. The hook adapter formats
// these into a human-readable, single-line message that the Git client
// surfaces via gRPC PermissionDenied.
type Violation struct {
	Predicate NeverPredicate
	RefName   string
	Reason    string
}

// Evaluate runs every Never predicate against every ref update and
// returns the matched violations. Order is stable: predicates iterated in
// policy order, updates iterated in input order.
//
// A nil context is invalid; callers must pass a non-nil context. A nil
// input.Files is permitted only when the policy contains no predicates
// that need file information (modify_path, push_binary_over_size); the
// evaluator returns an error in that case rather than silently passing.
func Evaluate(ctx context.Context, policy *Policy, input EvaluationInput) ([]Violation, error) {
	if policy == nil || policy.Empty || len(policy.Never) == 0 {
		return nil, nil
	}
	var out []Violation
	for _, pred := range policy.Never {
		for _, upd := range input.Updates {
			matched, reason, err := matchPredicate(ctx, pred, upd, input.Files)
			if err != nil {
				return nil, fmt.Errorf("agentsmd: evaluate %s on %s: %w", pred.Name, upd.RefName, err)
			}
			if matched {
				out = append(out, Violation{Predicate: pred, RefName: upd.RefName, Reason: reason})
			}
		}
	}
	return out, nil
}

// matchPredicate returns (matched, reason, err) for a single (predicate, update) pair.
func matchPredicate(ctx context.Context, pred NeverPredicate, upd RefUpdate, files FileResolver) (bool, string, error) {
	switch pred.Name {
	case PredicateDeleteBranch:
		if !branchMatches(pred.Selector.BranchGlob, upd.RefName) {
			return false, "", nil
		}
		if upd.NewOID != ZeroOID {
			return false, "", nil
		}
		return true, fmt.Sprintf("branch %q deletion blocked by Never rule (glob %q)", branchName(upd.RefName), pred.Selector.BranchGlob), nil

	case PredicatePushToBranch:
		if !branchMatches(pred.Selector.BranchGlob, upd.RefName) {
			return false, "", nil
		}
		return true, fmt.Sprintf("push to branch %q blocked by Never rule (glob %q)", branchName(upd.RefName), pred.Selector.BranchGlob), nil

	case PredicateForcePushToBranch:
		if !branchMatches(pred.Selector.BranchGlob, upd.RefName) {
			return false, "", nil
		}
		// Branch creations and deletions are not force pushes.
		if upd.OldOID == ZeroOID || upd.NewOID == ZeroOID {
			return false, "", nil
		}
		if files == nil {
			return false, "", fmt.Errorf("force_push_to_branch requires a FileResolver")
		}
		ff, err := files.IsFastForward(ctx, upd.OldOID, upd.NewOID)
		if err != nil {
			return false, "", err
		}
		if ff {
			return false, "", nil
		}
		return true, fmt.Sprintf("force push to %q blocked by Never rule (glob %q)", branchName(upd.RefName), pred.Selector.BranchGlob), nil

	case PredicateModifyPath:
		if files == nil {
			return false, "", fmt.Errorf("modify_path requires a FileResolver")
		}
		paths, err := files.Changed(ctx, upd.OldOID, upd.NewOID)
		if err != nil {
			return false, "", err
		}
		for _, p := range paths {
			if pathMatches(pred.Selector.PathGlob, p) {
				return true, fmt.Sprintf("path %q modification blocked by Never rule (glob %q)", p, pred.Selector.PathGlob), nil
			}
		}
		return false, "", nil

	case PredicatePushBinaryOverSize:
		if files == nil {
			return false, "", fmt.Errorf("push_binary_over_size requires a FileResolver")
		}
		paths, err := files.Changed(ctx, upd.OldOID, upd.NewOID)
		if err != nil {
			return false, "", err
		}
		for _, p := range paths {
			sz, err := files.Size(ctx, upd.NewOID, p)
			if err != nil {
				return false, "", err
			}
			if sz > pred.Selector.MaxBytes {
				return true, fmt.Sprintf("blob %q size %d > limit %d", p, sz, pred.Selector.MaxBytes), nil
			}
		}
		return false, "", nil

	default:
		// Unknown predicate names are dropped at parse time. If one slips
		// through here (e.g. a hand-constructed Policy), be safe: do not
		// match, do not error. The lint diagnostic at parse time is the
		// load-bearing surface.
		return false, "", nil
	}
}

// branchMatches checks whether a full ref name matches a branch glob.
// The glob is matched against the short branch name (refs/heads/<name>);
// non-branch refs (tags, notes) never match a branch predicate.
func branchMatches(glob, refName string) bool {
	const prefix = "refs/heads/"
	if !strings.HasPrefix(refName, prefix) {
		return false
	}
	name := refName[len(prefix):]
	return globMatch(glob, name)
}

// branchName returns the short branch name for human-readable messages.
// If refName is not a branch, it is returned unchanged.
func branchName(refName string) string {
	const prefix = "refs/heads/"
	if strings.HasPrefix(refName, prefix) {
		return refName[len(prefix):]
	}
	return refName
}

// pathMatches matches a repository-relative path against a glob. The
// glob grammar matches path.Match plus a leading-component "**" form:
// "infra/**" matches anything under infra/.
func pathMatches(glob, p string) bool {
	if strings.HasSuffix(glob, "/**") {
		prefix := strings.TrimSuffix(glob, "/**")
		return p == prefix || strings.HasPrefix(p, prefix+"/")
	}
	if glob == "**" {
		return true
	}
	ok, err := path.Match(glob, p)
	if err != nil {
		return false
	}
	return ok
}

// globMatch matches a short branch name against a glob. We support
// path.Match grammar; "*" does not cross "/" by stdlib semantics, which
// is the right choice for branch names like "release/v1".
func globMatch(glob, name string) bool {
	if glob == "" {
		return false
	}
	ok, err := path.Match(glob, name)
	if err != nil {
		return false
	}
	return ok
}
