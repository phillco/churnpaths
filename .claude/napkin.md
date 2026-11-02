# Napkin Runbook

## Safety and verification
1. **[2026-11-02] Tests must never alter live exclusion settings.**
   Do instead: fake native and shell entry points, confine XML/state writes to temporary fixtures, and run parity only with resolve or read-only modes.
2. **[2026-11-02] Python is the compatibility reference.**
   Do instead: preserve `python/` unchanged and compare decoded JSON plus exact ordinary text on identical disposable trees.
3. **[2026-11-02] Native builds need cgo on macOS.**
   Do instead: run build/vet/tests with default cgo and test the disabled-cgo fallback; keep the delivered executable ignored.
