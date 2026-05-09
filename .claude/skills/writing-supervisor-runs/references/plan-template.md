# Supervisor run plan — template

The plan is a markdown file with a YAML front-matter block followed by human-readable explanation. The agent reads the YAML; the prose is for humans reviewing the run.

Fill every `<<placeholder>>`. Do not add new top-level YAML keys — the agent ignores anything outside the schema below.

---

```yaml
---
# Identity
run_id: "<<YYYY-MM-DD>>"
sentinel: "SUPERVISOR-RUN-COMPLETE-<<YYYY-MM-DD>>"
spec: "docs/superpowers/specs/<<YYYY-MM-DD>>-supervisor-<<scope>>-design.md"
completion_report_path: "docs/superpowers/runs/<<YYYY-MM-DD>>-supervisor.completion.md"
log_path: "docs/superpowers/runs/<<YYYY-MM-DD>>-supervisor.log"

# Repo + worktrees
repo:
  root: "<<absolute path of primary checkout>>"
  default_branch: "<<main>>"
  worktrees_root: "<<absolute path, sibling of repo.root recommended>>"
  branch_pattern: "<<regex or glob: e.g. type/domain-short-description>>"
  identity_email: "<<expected git config user.email>>"

# Where per-issue artefacts live
paths:
  specs_dir: "docs/superpowers/specs"
  plans_dir: "docs/superpowers/plans"
  prompts_dir: "docs/superpowers/prompts"
  runs_dir:   "docs/superpowers/runs"

# Scope guardrails
scope:
  in_scope_domains:
    - "<<domain-1>>"
    - "<<domain-2>>"
  out_of_scope_domains:
    - "<<domain-N>>"
  open_arch_questions:
    - "<<question or area to dodge until resolved>>"
  invariant_docs:
    - "<<path to a doc where merge conflicts are auto-blockers>>"

# Concurrency
caps:
  concurrent_branch_cap: 4         # hard cap on in-flight branches owned by supervisor
  concurrent_subagent_cap: 6       # soft cap on parallel subagent dispatches

# Issues — one row per issue in scope
issues:
  - number: <<NN>>
    title: "<<issue title>>"
    domain: "<<free-text domain tag — used to look up subagent in subagent_map>>"
    priority: "<<p1|p2|p3>>"
    deps: []                       # list of issue numbers that must be MERGED first
    notes: "<<optional: special handling, e.g. 'doc-amendment-only, no impl branch'>>"
  - number: <<NN>>
    title: "<<...>>"
    domain: "<<...>>"
    priority: "<<...>>"
    deps: [<<NN>>, <<NN>>]
    notes: ""
  # ... repeat for every issue in scope ...

# Wave ordering — derived from issues[].deps but listed explicitly for the
# supervisor's parallelism math and for human review
waves:
  - wave: 0
    description: "no deps — all parallel"
    issues: [<<NN>>, <<NN>>, <<NN>>]
  - wave: 1
    description: "gates on Wave 0 issues being MERGED"
    issues: [<<NN>>]
    soft_coordination: "<<optional human note about issues that share scope but can ship independently>>"
  - wave: 2
    description: "gates on Wave 1"
    issues: [<<NN>>, <<NN>>]

# Subagent + skill matrix — by domain
# Special-case rows can use issues: [<NN>] instead of domain.
subagent_map:
  - domain: "<<domain-1>>"
    implementation_subagent: "<<subagent-name>>"
    mandatory_pre_commit_skills:
      - "<<skill-name-1>>"
      - "<<skill-name-2>>"
  - domain: "<<domain-2>>"
    implementation_subagent: "<<subagent-name>>"
    mandatory_pre_commit_skills:
      - "<<skill-name>>"
  - issues: [<<NN>>]                    # special handling for one issue
    implementation_subagent: "<<subagent-name>>"
    mandatory_pre_commit_skills:
      - "<<skill-name>>"
    notes: "<<why this issue is special>>"

# Self-review battery — runs in parallel before every `gh pr create`
self_review_battery:
  - "<<reviewer-subagent-1>>"           # e.g. code-reviewer
  - "<<reviewer-subagent-2>>"           # e.g. silent-failure-hunter
  - "<<reviewer-subagent-3>>"           # e.g. architecture-review
  # always invoke
  conditional:
    - subagent: "<<reviewer-subagent-4>>"
      when: "<<condition, e.g. 'new public type added'>>"
    - subagent: "<<reviewer-subagent-5>>"
      when: "<<condition, e.g. 'comments added'>>"

# Interactive skills the supervisor invokes for PENDING_DESIGN / PENDING_PLAN
interactive_skills:
  brainstorming: "<<skill name, e.g. superpowers:brainstorming>>"
  plan_writing:  "<<skill name, e.g. superpowers:writing-plans>>"
  debugging:     "<<skill name, e.g. superpowers:systematic-debugging>>"

# Project-specific commands
pre_push_gates:
  - "<<command 1>>"                     # e.g. "go build ./..."
  - "<<command 2>>"                     # e.g. "golangci-lint run"
  - "<<command 3>>"                     # e.g. "go test -race ./..."

ci_gates:
  - "<<required check name 1>>"         # GitHub Actions check name
  - "<<required check name 2>>"

# PR quality bar — string-fill the rules. Wording is project-specific; the
# agent enforces "every required field present" rather than reading these as code.
pr_quality_bar:
  title_format: "<<e.g. [Domain] Short imperative — mirrors issue title>>"
  branch_format: "<<e.g. type/domain-short-description>>"
  required_body_sections:
    - "Summary (2-4 bullets)"
    - "ADR-impact (creating | amending | conforming | none)"
    - "Test plan (checklist matching plan acceptance criteria)"
    - "Closes #N (mandatory)"
    - "Spec + plan cross-links"
    - "Self-review block (collapsed <details>)"
    - "Co-author trailer"
  commit_convention: "<<e.g. Conventional Commits>>"
  co_author_trailer: "<<exact line, e.g. Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>>>"
  no_emoji: true
  closes_issue_required: true

# Termination
termination:
  total_issues: <<N>>                   # must equal len(issues)
  follow_up_issues_allowed: true        # if true, follow-up issues filed during the
                                        # run also count toward termination
  conditions_all_must_hold:
    - "every issue MERGED (plus any registered follow-ups)"
    - "gh pr list --state open --author @me is empty"
    - "git worktree list shows only the primary worktree"
    - "completion report at completion_report_path is committed"
---
```

## Prose section (after the YAML)

Add a short markdown body explaining the run for human reviewers — it's optional but recommended. Suggested headings:

```markdown
# <<Run title — e.g. Wave 3 supervisor run>>

## Why now
<<one paragraph: what triggered this wave, what's been merged before>>

## What ships in this run
<<bullets — high-level outcomes the run is committing to>>

## What deliberately doesn't ship
<<bullets — out-of-scope items and why>>

## Open questions deferred
<<bullets — open arch questions this run dodges (matches scope.open_arch_questions)>>

## Soft coordination notes
<<call out any waves[].soft_coordination entries with extra context>>
```

## Conventions

- **`<<placeholder>>`** is a literal marker. Grep your filled plan for `<<` before launch — every occurrence must be gone.
- **Issue numbers are integers**, not strings.
- **Empty arrays must be `[]`**, not omitted, so the YAML parses identically across runs.
- **Wave-0 issues have `deps: []`**; the agent uses this to bootstrap the topological sort.
- **Domain strings are free-text** — the agent matches `issues[].domain` against `subagent_map[].domain` as exact strings. Pick a vocabulary and stick to it across waves.
- **`sentinel` is the literal string** the agent emits to terminate. It must include the run-id and match `--completion-promise` on the launch line character-for-character.
- **`pre_push_gates` and `ci_gates`** can be empty lists for projects without those gates, but at least one of them must be non-empty (otherwise the supervisor will merge unverified code).

## Validation

Before launch, run these checks:

```bash
# YAML parses
yq eval '.' "<<plan path>>" > /dev/null

# Every issue has a subagent
yq eval '.issues[].number as $n | (.subagent_map[] | (.domain // (.issues[]?))) as $d' "<<plan path>>"

# Sentinel matches run_id
test "$(yq eval '.sentinel' <plan>)" = "SUPERVISOR-RUN-COMPLETE-$(yq eval '.run_id' <plan>)"

# total_issues matches count
test "$(yq eval '.termination.total_issues' <plan>)" -eq "$(yq eval '.issues | length' <plan>)"

# No leftover placeholders
! grep -q '<<' "<<plan path>>"
```

If any check fails, fix the plan before launch — the agent will not.
