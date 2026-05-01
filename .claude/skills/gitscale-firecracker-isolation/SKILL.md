---
name: gitscale-firecracker-isolation
description: Use when adding or modifying CI runner code, sandbox setup for agent-executed jobs, container or VM lifecycle management, or anything in plane/workflow that spawns user / agent code in an isolated environment. Triggers on imports of docker/runc/gvisor/containerd/podman, on shell-out to `docker run`, on adding container-orchestration code paths, and on user questions like "can I use Docker for this?", "what's the CI sandbox?", "is gVisor enough?". Catches the silent erosion of the hardware-isolation guarantee — once Docker creeps in alongside Firecracker, an attacker only needs to find the weakest path, and the security bound becomes the worst of the two, not the best.
---

# GitScale Firecracker Isolation

## Overview

GitScale runs untrusted code (CI jobs, agent-executed scripts, user-provided test runners) inside Firecracker microVMs. The choice is deliberate: Firecracker provides a hardware-virtualization boundary (KVM), not a kernel-namespacing boundary. Docker, runc, podman, containerd, even gVisor share the host kernel; an exploit in the kernel surface area escapes the sandbox.

Once Firecracker is the standard, mixing in container-based isolation breaks the guarantee — any single container-isolated path becomes the new weakest link, and the security model is no longer "hardware boundary."

**Core principle:** every untrusted-code execution path goes through Firecracker. Container runtimes, even when convenient, do not appear in CI runner code, agent-execution code, or anywhere user-provided code is materialized.

## When to Use

Trigger on **any** of:

- An import of `github.com/docker/docker`, `github.com/containerd/...`, `github.com/opencontainers/runc`, `github.com/google/gvisor`, `github.com/containers/podman`, or any client library for those runtimes
- Shell-outs to `docker`, `podman`, `nerdctl`, `runc`, `runsc` (gVisor's runtime)
- New code under `plane/workflow/runner/...` or `plane/workflow/sandbox/...` that doesn't go through `firecracker-go-sdk` or the project's `pkg/microvm` wrapper
- Configuration files (`docker-compose.yml`, `Dockerfile` for runtime images vs. build images)
- The user asks "can I use Docker for this?", "is gVisor sufficient?", "what's the CI sandbox?", "can I run this in a container?"

**Don't trigger** for:

- Build-time Docker (CI build images for the GitScale services themselves are fine — those run in trusted CI pipelines, not user-code execution paths). The rule is about **runtime isolation of untrusted code**, not about how GitScale itself is built.
- Tests of microVM code that mock the Firecracker API.
- Documentation that *mentions* Docker/gVisor for comparison purposes.

## The rule

| Surface | Allowed |
|---|---|
| Untrusted code execution (CI job, agent script, user runner) | **Firecracker microVM only** |
| Trusted code execution (GitScale services themselves) | Containers fine (Kubernetes / Nomad / etc.) |
| Build pipeline for GitScale itself | Containers fine (BuildKit, etc.) |
| Local developer environment | Whatever the developer prefers; not enforced |

The dividing line is **does this run code we don't trust?** Yes → Firecracker. No → unrestricted.

## Workflow

1. **Identify the path** the diff lands in. Is it an untrusted-code execution surface?
2. **For execution surfaces**: scan for forbidden imports and shell-outs.
3. **For each finding**, propose the Firecracker-based replacement (usually via `pkg/microvm` or `firecracker-go-sdk`).
4. **For "convenience" container code**: ask whether the convenience is worth the security regression. The answer is almost always no for execution paths.
5. **Output a verdict** with file:line citations.

## Output Format

```
firecracker-isolation: <ok | violation>
Path classification: <untrusted-execution | trusted-build | unclear>
Forbidden imports/shell-outs:
  <file:line — what — why blocked>
Fix: <concrete replacement>
```

## Example

**Input diff:**

```go
// plane/workflow/runner/run.go
import "github.com/docker/docker/client" // ❌

func RunCIJob(ctx context.Context, image string, cmd []string) error {
    cli, _ := client.NewClientWithOpts(client.FromEnv)
    resp, err := cli.ContainerCreate(ctx, &container.Config{
        Image: image, Cmd: cmd,
    }, nil, nil, nil, "")
    // ...
}
```

**Verdict:**

```
firecracker-isolation: violation
Path classification: untrusted-execution (CI job runner)
Forbidden imports/shell-outs:
  plane/workflow/runner/run.go:1 — imports docker/docker/client — Docker shares
    the host kernel; CI jobs are untrusted code and require a hardware boundary.
  plane/workflow/runner/run.go:5 — ContainerCreate — same reason.
Fix:
  Replace with the project's microvm wrapper:
    vm, err := microvm.New(ctx, microvm.Config{
      Image: image,
      Cmd:   cmd,
      // resource limits, network policy, etc.
    })
    if err != nil { return err }
    defer vm.Close()
    return vm.Run(ctx)
  This wraps firecracker-go-sdk with the project's standard image format,
  network policy, and resource limits. See plane/workflow/sandbox/microvm/
  for examples.
```

## Common Mistakes

| Mistake | Fix |
|---|---|
| "gVisor is also a sandbox, that should be fine" | gVisor sits between the application and the host kernel via a userspace re-implementation of syscalls. The bound is software, not hardware, and the attack surface is gVisor itself. We chose hardware-boundary deliberately. |
| Adding Docker for the convenience of `docker pull` to fetch images | Use the OCI image format with Firecracker's image-loader. Pulling and converting can happen out-of-band. |
| Mixing Firecracker for untrusted code with Docker for "trusted parts" of the same job | The job is one unit. If untrusted code can reach the Docker side, the Docker side is now an escape route. Single isolation primitive per job. |
| Using `os/exec` to run `docker run` from CI runner code as a "shortcut" | Same problem dressed differently. Forbidden. |
| Treating the CI image as trusted because it was built in our pipeline | The image runs *user* commands. The image format being known doesn't make the contents safe. |
| Disabling Firecracker in dev "because microVMs are slow on my laptop" | Use a smaller image or fewer test cases in dev. Don't change the isolation primitive — that's the bug we're trying to prevent in production. |

## Why This Matters

The reason GitScale runs Firecracker rather than Docker for CI is that the platform exists to run other people's code at scale. The threat model assumes some fraction of that code is malicious. A kernel exploit at host level — and the Linux kernel surface is large — escapes a container. It does not escape a microVM, because the microVM has its own kernel and the host is reached only through KVM and a tiny vsock interface.

When this guarantee erodes — even in one path — the platform's security claim weakens to whatever the worst path provides. Customers and auditors care about "what runs my untrusted job?" The answer must be one word: Firecracker. Anything else turns it into a 200-word explanation, and the explanation is what gets cut for time during the next compliance review.

The diff is the cheapest place to defend the guarantee. Once Docker imports land in production, removing them is months of work; preventing them in review is one comment.
