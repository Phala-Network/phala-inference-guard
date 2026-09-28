# Releases

`main` integrates maintained code. A release tag names a verified commit in
`main` history; tags and existing image versions are immutable. Development
branches and historical topology variants are not substitutes for the standard
release. Preserve published variants for traceability rather than merging
incompatible behavior solely to simplify branch lists.

## New versions

1. Update source version declarations, tests and documentation together; run
   affected checks and the Linux production image contract.
2. Integrate the validated commit into `main` and create its annotated `vX.Y.Z`
   tag. Confirm tag ancestry and the exact version/revision relationship.
3. The tag workflow builds once, validates that local image and pushes the same
   image. It refuses to overwrite an existing registry version. Read back and
   record the published digest, source commit, validation and known limitations
   in release notes. Deployment acceptance is a separate operation.

The image-contract check validates packaging and native library support; it does
not replace service startup, protocol or target-specific acceptance.

## Backfilling historical tags

Read the existing registry digest and source revision first. Tag that exact
source, preserving its relationship to `main`; if a source archive is the only
record, recover and verify its exact bytes and document the new commit mapping.
An existing published image must not be rebuilt just to add a Git tag.

Older commits contain the legacy tag publisher without the overwrite guard.
Before tagging those commits, inspect their workflow and prevent that publisher
from running; restore any temporarily changed workflow state afterward. Verify
the remote peeled tag and unchanged registry digest. Do not move an existing tag
or describe a failed historical candidate as an accepted production release.

Historical build-input artifacts and SHA aliases are recorded alongside their
version; they do not each require a separate source release. Prior evidence is
indexed in [HISTORY.md](HISTORY.md).
