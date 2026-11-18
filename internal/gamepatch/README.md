# HDIFF13 rebuild package

This package implements the uncompressed `HDIFF13&\0` format independently in Go. It accepts four uncompressed sections, tagged base-128 offsets, signed source distances, and all four RLE delta forms. It has no network, download, installation, or transaction dependencies.

`Apply(ctx, Options)` requires `OldExpect`, `PatchExpect`, and `Expect` identities (`gameres.Object`, lowercase MD5 plus size). It validates the complete patch before reading the old file, validates the complete source, reconstructs into a private partial file, and verifies the complete output before publication. The caller must serialize access against input writers and place `OutputPath` in its rebuild workspace. Inputs are opened read-only through `os.Root`. Output must be new: existing files, hard links, symbolic links, case aliases, and dot aliases are rejected without modifying them.

Publication uses an exclusive hard link from the validated private partial file, then removes the private name. This prevents incomplete bytes at `OutputPath` and avoids overwrite races. The output filesystem must support hard links (for example, NTFS); unsupported filesystems return their underlying error. This is a temporary second name for the package's own new file, never a link to an old installation file. A crash can leave a private `.gamepatch-*.partial` orphan. Callers own recovery cleanup of abandoned workspace entries and must never treat their existence as verification.

The decoder uses a reusable 1 MiB transfer/hash buffer, a 32 KiB RLE scratch buffer, and fixed small section readers. It checks cancellation during validation, at transfer boundaries no larger than 1 MiB, and during raw delta processing. This bounds decoder buffers independently of file size; it is not a measured real-patch working-set claim.

`ApplyChain(ctx, steps, workdir, report) (string, error)` accepts one to four `Step` values. Every step declares `Old`, `Patch`, and `New` identities and a `PatchPath`. Only the first step supplies `OldPath`; later inputs are exclusively the preceding validated output, and adjacent identities must match. The workspace must already exist. Each invocation owns an exclusive fresh subdirectory. Successful next steps remove their predecessor. Failures remove that invocation's subdirectory, preserving unrelated files, patch blobs, old inputs, and prior orphan directories. Success returns the validated final path; the caller owns that file and enclosing directory. Reports use zero-based step indices and begin each step with `(step, 0, New.Size)`.

`ApplyChainMeasured` has the same ownership and reporting contract and returns `IOStats` in addition to the output path and error. `ReadBytes` and `WrittenBytes` count actual `ReadAt`/`Write` results across old, patch, and output files, including whole-file verification and buffered header reads. Counts include work completed before cancellation or failure. They exclude metadata operations and filesystem allocation; wall duration belongs to the caller. Existing `Apply` and `ApplyChain` signatures are preserved.

Synthetic tests run with:

```powershell
pwsh -File scripts/go.ps1 test ./internal/gamepatch/ -count=1
pwsh -File scripts/go.ps1 test ./internal/gamepatch/ -run TestApplyCrashRecovery -count=1
```

No real fixtures are included. The product patch capability is enabled only after the private D1–D6 acceptance completed with official CDN resources, four real patch samples including the 10 GB class sample, a real two-step chain, cancellation, corruption and fallback checks. Synthetic tests establish local behavior only, including two-step chains, cancellation, source preservation, corrupt offset rejection, exclusive output, and restart without trusting orphan data. Private acceptance measurements remain under `artifacts/download-spec/`.

## Optional private compatibility interface

`TestApplyPrivateFixtures` is skipped unless both `ODDWARD_PATCH_FIXTURES` and `ODDWARD_PATCH_FIXTURES_AUTHORIZED=1` are explicitly set. It also skips under `-short`. Setting the authorization variable is an explicit action for a separately authorized local run; ordinary package checks must not enable it. The implementation work did not run this interface on real data.

Prepare a private fixture directory containing isolated copies of old inputs and patch blobs, with no shared hard links to an installed game. Do not use an active installation as the fixture root. All paths in `manifest.json` must be local relative paths under that root. Expected identities must come from trusted metadata. A manifest can list independent single-step cases and real multi-step chains:

```json
{
  "cases": [
    {
      "name": "private-case-description",
      "steps": [
        {
          "old_path": "inputs/old.bin",
          "patch_path": "patches/first.bin",
          "old": {"md5": "<32 lowercase hexadecimal characters>", "size": 123},
          "patch": {"md5": "<32 lowercase hexadecimal characters>", "size": 456},
          "new": {"md5": "<32 lowercase hexadecimal characters>", "size": 789}
        },
        {
          "patch_path": "patches/second.bin",
          "old": {"md5": "<same identity as preceding new>", "size": 789},
          "patch": {"md5": "<32 lowercase hexadecimal characters>", "size": 234},
          "new": {"md5": "<32 lowercase hexadecimal characters>", "size": 890}
        }
      ]
    }
  ]
}
```

After explicit authorization for that isolated dataset, a local operator can run:

```powershell
$env:ODDWARD_PATCH_FIXTURES = '<private isolated fixture directory>'
$env:ODDWARD_PATCH_FIXTURES_AUTHORIZED = '1'
pwsh -File scripts/go.ps1 test ./internal/gamepatch/ -run '^TestApplyPrivateFixtures$' -count=1 -v
Remove-Item Env:ODDWARD_PATCH_FIXTURES
Remove-Item Env:ODDWARD_PATCH_FIXTURES_AUTHORIZED
```

Outputs go only to Go test temporary directories. Inputs and patches are not modified. The test validates each chain and independently rechecks the returned final file, logging step counts, target size, duration, and output MiB/s. It does not certify real-fixture coverage counts, a 10 GB sample, peak process working set, installation recovery, UI, or Full fallback; those require the separate acceptance procedure. Keep raw paths, manifests, binaries, and local evidence out of public source and commits.
