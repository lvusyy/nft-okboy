package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// upgradeRepo is the GitHub repo whose Releases host the prebuilt binaries.
const upgradeRepo = "lvusyy/nft-okboy"

// ghMirrors are tried in order for every GitHub download so the upgrade works
// from networks where github.com is slow/blocked. "" is the direct path; the
// rest are public CN-friendly reverse proxies (see ghproxy.link for live ones).
var ghMirrors = []string{"", "https://ghfast.top/", "https://gh-proxy.com/"}

// CmdUpgrade self-updates the running okboy binary to the latest GitHub release
// (or a pinned --version). It is the day-2 counterpart of deploy/install.sh:
//
//  1. resolve the target tag (latest release, or --version);
//  2. back up the DB first — a newer binary may migrate the schema forward, so a
//     rollback needs the pre-upgrade copy;
//  3. download the release asset + its .sha256 (mirror fallback) and verify;
//  4. atomically swap the running binary, keeping <exe>.bak;
//  5. health-check the new binary and roll back on failure;
//  6. restart the systemd service (best-effort, only if managed by systemd).
//
// Only linux/amd64 has published binaries; other platforms must build from source.
func CmdUpgrade(cfgPath, version string, args []string) error {
	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	check := fs.Bool("check", false, "Only report whether a newer release exists; do not install")
	target := fs.String("version", "", "Install this exact tag (e.g. v0.2.0) instead of the latest")
	noRestart := fs.Bool("no-restart", false, "Do not restart the okboy service after upgrading")
	if err := fs.Parse(args); err != nil {
		return err
	}

	asset := assetForHost()
	if asset == "" {
		return fmt.Errorf("no prebuilt binary for %s/%s; build from source (32-bit ARM: use deploy/install.sh)",
			runtime.GOOS, runtime.GOARCH)
	}

	want := *target
	if want == "" {
		latest, err := latestReleaseTag(upgradeRepo)
		if err != nil {
			return fmt.Errorf("resolve latest release: %w (or pass --version vX.Y.Z)", err)
		}
		want = latest
	}
	fmt.Printf("Current: %s   Target: %s\n", displayVersion(version), want)

	if *check {
		if sameVersion(version, want) {
			fmt.Println("Already up to date.")
		} else {
			fmt.Printf("A newer release is available: %s\n", want)
		}
		return nil
	}
	if *target == "" && sameVersion(version, want) {
		fmt.Println("Already up to date.")
		return nil
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("upgrade must run as root (try: sudo okboy upgrade)")
	}

	// Resolve symlinks so the staged binary lands in the same directory as the
	// real file — os.Rename is only atomic within one filesystem.
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate self: %w", err)
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}

	// DB backup before any swap (rollback safety for forward migrations). Reuses
	// CmdBackup so retention/checksum behave identically to a manual backup.
	fmt.Println("Backing up database…")
	if berr := CmdBackup(cfgPath, nil); berr != nil {
		fmt.Fprintf(os.Stderr, "warning: db backup failed (continuing): %v\n", berr)
	}

	fmt.Printf("Downloading %s %s…\n", asset, want)
	data, err := ghDownload(upgradeRepo, want, asset)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	if verr := verifyChecksum(upgradeRepo, want, asset, data); verr != nil {
		return verr
	}

	// Stage in the target directory, then atomically swap, keeping a .bak.
	dir := filepath.Dir(exe)
	tmp := filepath.Join(dir, ".okboy.upgrade.tmp")
	if werr := os.WriteFile(tmp, data, 0o755); werr != nil {
		return fmt.Errorf("write staged binary: %w", werr)
	}
	defer os.Remove(tmp) // no-op once the rename below succeeds

	bak := exe + ".bak"
	if cerr := copyFile(exe, bak, 0o755); cerr != nil {
		return fmt.Errorf("back up current binary: %w", cerr)
	}
	if rerr := os.Rename(tmp, exe); rerr != nil {
		return fmt.Errorf("install new binary: %w", rerr)
	}

	// Sanity-check the freshly installed binary runs at all (catches a corrupt or
	// wrong-arch download) before touching the service; restore the .bak on failure.
	if out, herr := exec.Command(exe, "--version").CombinedOutput(); herr != nil {
		_ = os.Rename(bak, exe)
		return fmt.Errorf("new binary failed to run (%v: %s); rolled back",
			herr, strings.TrimSpace(string(out)))
	}

	if !*noRestart {
		if rerr := restartService("okboy"); rerr != nil {
			// Not systemd-managed (dev / manual run): the new binary is staged, but
			// starting it is the operator's job — nothing to verify or roll back.
			fmt.Fprintf(os.Stderr, "warning: could not restart service (start it manually): %v\n", rerr)
		} else if !waitServiceActive("okboy", 12*time.Second) {
			// The service restarted but never reached active — e.g. the new binary
			// crashes on `serve`. A bare `--version` check (above) cannot catch that,
			// so verify the live service and roll back to the previous binary.
			_ = os.Rename(bak, exe)
			_ = exec.Command("systemctl", "restart", "okboy").Run()
			return fmt.Errorf("upgraded binary did not bring the service up; rolled back to the previous version")
		} else {
			fmt.Println("Service restarted and healthy.")
		}
	}

	fmt.Printf("Upgraded okboy %s → %s.  Previous binary kept at %s\n",
		displayVersion(version), want, bak)
	return nil
}

// displayVersion renders the build version for display (a leading "v" for a real
// semver, or "dev" unchanged).
func displayVersion(v string) string {
	if v == "" || v == "dev" {
		return "dev"
	}
	return "v" + strings.TrimPrefix(v, "v")
}

// sameVersion reports whether the build version already matches tag (ignoring a
// leading "v"). A "dev" build is never considered up to date.
func sameVersion(version, tag string) bool {
	if version == "" || version == "dev" {
		return false
	}
	return strings.TrimPrefix(version, "v") == strings.TrimPrefix(tag, "v")
}

// httpClient is the shared client for upgrade downloads (bounded so a stalled
// mirror fails over instead of hanging).
func httpClient() *http.Client { return &http.Client{Timeout: 90 * time.Second} }

// latestReleaseTag resolves the newest release tag via the GitHub API.
func latestReleaseTag(repo string) (string, error) {
	req, err := http.NewRequest(http.MethodGet,
		"https://api.github.com/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "okboy-upgrade")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := httpClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github api returned %d", resp.StatusCode)
	}
	var r struct {
		TagName string `json:"tag_name"`
	}
	if derr := json.NewDecoder(resp.Body).Decode(&r); derr != nil {
		return "", derr
	}
	if r.TagName == "" {
		return "", fmt.Errorf("empty tag_name in release response")
	}
	return r.TagName, nil
}

// ghDownload fetches a release asset, trying each mirror prefix until one serves
// HTTP 200.
func ghDownload(repo, tag, asset string) ([]byte, error) {
	path := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", repo, tag, asset)
	var lastErr error
	for _, m := range ghMirrors {
		resp, err := httpClient().Get(m + path)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = fmt.Errorf("%s%s → HTTP %d", m, path, resp.StatusCode)
			continue
		}
		b, rerr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if rerr != nil {
			lastErr = rerr
			continue
		}
		return b, nil
	}
	return nil, lastErr
}

// assetForHost maps the running platform to its published release asset name, or
// "" when no prebuilt binary is published for it. The release ships one static
// binary per linux arch named "okboy-linux-<arch>".
func assetForHost() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	switch runtime.GOARCH {
	case "amd64", "arm64", "386", "loong64", "ppc64le", "riscv64", "s390x":
		return "okboy-linux-" + runtime.GOARCH
	default:
		// "arm" cannot be disambiguated into armv6/armv7 at runtime — deploy/
		// install.sh resolves that from `uname -m`.
		return ""
	}
}

// verifyChecksum downloads the release's combined SHA256SUMS file and checks data
// against the line for asset. A missing SHA256SUMS is a warning, not a hard fail.
func verifyChecksum(repo, tag, asset string, data []byte) error {
	sums, err := ghDownload(repo, tag, "SHA256SUMS")
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: no published SHA256SUMS; skipping verification")
		return nil
	}
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[1] == asset { // "<hex>  <file>"
			want = f[0]
			break
		}
	}
	if want == "" {
		return fmt.Errorf("SHA256SUMS has no entry for %s — aborting", asset)
	}
	sum := sha256.Sum256(data)
	if !strings.EqualFold(want, hex.EncodeToString(sum[:])) {
		return fmt.Errorf("checksum mismatch for %s — aborting", asset)
	}
	fmt.Println("Checksum verified.")
	return nil
}

// copyFile copies src to dst with the given mode (used for the rollback .bak).
func copyFile(src, dst string, mode os.FileMode) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, mode)
}

// waitServiceActive polls `systemctl is-active <name>` until it reports "active"
// or the timeout elapses. A freshly restarted okboy needs a moment to open its DB,
// ensure the nft base table, and bind its port, so a single immediate check would
// race the startup.
func waitServiceActive(name string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		out, _ := exec.Command("systemctl", "is-active", name).Output()
		if strings.TrimSpace(string(out)) == "active" {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(1500 * time.Millisecond)
	}
}

// restartService restarts a systemd unit, but only when systemd actually manages
// it — on a non-systemd or dev host this returns an error the caller downgrades
// to a warning instead of failing the whole upgrade.
func restartService(name string) error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return fmt.Errorf("systemctl not found")
	}
	if err := exec.Command("systemctl", "is-enabled", name).Run(); err != nil {
		return fmt.Errorf("service %q not managed by systemd", name)
	}
	return exec.Command("systemctl", "restart", name).Run()
}
