package migration

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFakeBinary(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func makeProvenanceRecord(t *testing.T, stateRoot, oldBin, newBin, configPath string) ProvenanceRecord {
	t.Helper()
	oldHash := sha256Hex([]byte("old-binary"))
	newHash := sha256Hex([]byte("new-binary"))
	configData := []byte("listen:\n  host: 127.0.0.1\n  port: 8787\n")
	if err := os.WriteFile(configPath, configData, 0o600); err != nil {
		t.Fatal(err)
	}
	configHash := sha256Hex(configData)
	return ProvenanceRecord{
		OldBinaryPath:       oldBin,
		OldBinaryHash:       oldHash,
		InstalledBinaryPath: newBin,
		InstalledBinaryHash: newHash,
		ServiceKind:         "background-daemon",
		ConfigPath:          configPath,
		ConfigHash:          configHash,
		CreatedAt:           "2025-01-01T00:00:00Z",
		// Background-daemon conditional provenance: PID and executable.
		BackgroundDaemonPID: 12345,
		BackgroundDaemonExe: newBin,
	}
}

func TestWriteAndReadProvenance(t *testing.T) {
	stateRoot := t.TempDir()
	oldBin := writeFakeBinary(t, filepath.Join(stateRoot, "old-bin"), "old-binary")
	newBin := writeFakeBinary(t, filepath.Join(stateRoot, "new-bin"), "new-binary")
	configPath := filepath.Join(stateRoot, "config.yaml")

	rec := makeProvenanceRecord(t, stateRoot, oldBin, newBin, configPath)

	if err := WriteProvenance(stateRoot, rec); err != nil {
		t.Fatalf("WriteProvenance: %v", err)
	}

	// Verify file permissions.
	info, err := os.Stat(filepath.Join(stateRoot, provenanceFileName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("provenance file mode %o is not private", info.Mode().Perm())
	}

	got, err := ReadProvenance(stateRoot)
	if err != nil {
		t.Fatalf("ReadProvenance: %v", err)
	}
	if got == nil {
		t.Fatal("expected provenance record, got nil")
	}
	if got.InstalledBinaryPath != newBin {
		t.Fatalf("installed binary path = %q, want %q", got.InstalledBinaryPath, newBin)
	}
	if got.ConfigHash != rec.ConfigHash {
		t.Fatalf("config hash mismatch")
	}
}

func TestReadProvenanceAbsent(t *testing.T) {
	stateRoot := t.TempDir()
	got, err := ReadProvenance(stateRoot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for absent provenance, got %+v", got)
	}
}

func TestConsumeProvenance(t *testing.T) {
	stateRoot := t.TempDir()
	oldBin := writeFakeBinary(t, filepath.Join(stateRoot, "old-bin"), "old-binary")
	newBin := writeFakeBinary(t, filepath.Join(stateRoot, "new-bin"), "new-binary")
	configPath := filepath.Join(stateRoot, "config.yaml")

	rec := makeProvenanceRecord(t, stateRoot, oldBin, newBin, configPath)
	if err := WriteProvenance(stateRoot, rec); err != nil {
		t.Fatal(err)
	}

	if err := ConsumeProvenance(stateRoot); err != nil {
		t.Fatalf("ConsumeProvenance: %v", err)
	}

	// Verify file is gone.
	if _, err := os.Stat(filepath.Join(stateRoot, provenanceFileName)); !os.IsNotExist(err) {
		t.Fatalf("provenance file should be removed")
	}

	// Consuming again should be a no-op (not an error).
	if err := ConsumeProvenance(stateRoot); err != nil {
		t.Fatalf("ConsumeProvenance twice: %v", err)
	}
}

func TestValidateProvenanceValid(t *testing.T) {
	stateRoot := t.TempDir()
	oldBin := writeFakeBinary(t, filepath.Join(stateRoot, "old-bin"), "old-binary")
	newBin := writeFakeBinary(t, filepath.Join(stateRoot, "new-bin"), "new-binary")
	configPath := filepath.Join(stateRoot, "config.yaml")

	rec := makeProvenanceRecord(t, stateRoot, oldBin, newBin, configPath)
	if err := WriteProvenance(stateRoot, rec); err != nil {
		t.Fatal(err)
	}

	err := ValidateProvenance(&rec, ProvenanceValidation{
		InstalledBinaryPath: newBin,
		ConfigPath:          configPath,
	})
	if err != nil {
		t.Fatalf("expected valid provenance, got error: %v", err)
	}
}

func TestValidateProvenanceBinaryHashMismatch(t *testing.T) {
	stateRoot := t.TempDir()
	oldBin := writeFakeBinary(t, filepath.Join(stateRoot, "old-bin"), "old-binary")
	newBin := writeFakeBinary(t, filepath.Join(stateRoot, "new-bin"), "new-binary")
	configPath := filepath.Join(stateRoot, "config.yaml")

	rec := makeProvenanceRecord(t, stateRoot, oldBin, newBin, configPath)

	// Change the binary after record creation.
	if err := os.WriteFile(newBin, []byte("different-binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := ValidateProvenance(&rec, ProvenanceValidation{
		InstalledBinaryPath: newBin,
		ConfigPath:          configPath,
	})
	if err == nil {
		t.Fatal("expected error for binary hash mismatch")
	}
}

func TestValidateProvenanceConfigHashMismatch(t *testing.T) {
	stateRoot := t.TempDir()
	oldBin := writeFakeBinary(t, filepath.Join(stateRoot, "old-bin"), "old-binary")
	newBin := writeFakeBinary(t, filepath.Join(stateRoot, "new-bin"), "new-binary")
	configPath := filepath.Join(stateRoot, "config.yaml")

	rec := makeProvenanceRecord(t, stateRoot, oldBin, newBin, configPath)

	// Edit the config after record creation.
	if err := os.WriteFile(configPath, []byte("listen:\n  port: 9999\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := ValidateProvenance(&rec, ProvenanceValidation{
		InstalledBinaryPath: newBin,
		ConfigPath:          configPath,
	})
	if err == nil {
		t.Fatal("expected error for config hash mismatch")
	}
}

func TestValidateProvenanceBinaryPathMismatch(t *testing.T) {
	stateRoot := t.TempDir()
	oldBin := writeFakeBinary(t, filepath.Join(stateRoot, "old-bin"), "old-binary")
	newBin := writeFakeBinary(t, filepath.Join(stateRoot, "new-bin"), "new-binary")
	configPath := filepath.Join(stateRoot, "config.yaml")

	rec := makeProvenanceRecord(t, stateRoot, oldBin, newBin, configPath)

	otherBin := writeFakeBinary(t, filepath.Join(stateRoot, "other-bin"), "new-binary")

	err := ValidateProvenance(&rec, ProvenanceValidation{
		InstalledBinaryPath: otherBin,
		ConfigPath:          configPath,
	})
	if err == nil {
		t.Fatal("expected error for binary path mismatch")
	}
}

func TestValidateProvenanceConfigPathMismatch(t *testing.T) {
	stateRoot := t.TempDir()
	oldBin := writeFakeBinary(t, filepath.Join(stateRoot, "old-bin"), "old-binary")
	newBin := writeFakeBinary(t, filepath.Join(stateRoot, "new-bin"), "new-binary")
	configPath := filepath.Join(stateRoot, "config.yaml")

	rec := makeProvenanceRecord(t, stateRoot, oldBin, newBin, configPath)

	otherConfig := filepath.Join(stateRoot, "other.yaml")
	if err := os.WriteFile(otherConfig, []byte("different"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := ValidateProvenance(&rec, ProvenanceValidation{
		InstalledBinaryPath: newBin,
		ConfigPath:          otherConfig,
	})
	if err == nil {
		t.Fatal("expected error for config path mismatch")
	}
}

func TestValidateProvenanceMissingFields(t *testing.T) {
	stateRoot := t.TempDir()
	newBin := writeFakeBinary(t, filepath.Join(stateRoot, "new-bin"), "new-binary")
	configPath := filepath.Join(stateRoot, "config.yaml")

	rec := makeProvenanceRecord(t, stateRoot, "", newBin, configPath)
	rec.OldBinaryPath = ""
	rec.OldBinaryHash = ""

	err := ValidateProvenance(&rec, ProvenanceValidation{
		InstalledBinaryPath: newBin,
		ConfigPath:          configPath,
	})
	if err == nil {
		t.Fatal("expected error for missing old binary fields")
	}
}

func TestValidateProvenanceNilRecord(t *testing.T) {
	err := ValidateProvenance(nil, ProvenanceValidation{})
	if err == nil {
		t.Fatal("expected error for nil record")
	}
}

func TestProvenanceFileIsPrivate(t *testing.T) {
	stateRoot := t.TempDir()
	oldBin := writeFakeBinary(t, filepath.Join(stateRoot, "old-bin"), "old-binary")
	newBin := writeFakeBinary(t, filepath.Join(stateRoot, "new-bin"), "new-binary")
	configPath := filepath.Join(stateRoot, "config.yaml")

	rec := makeProvenanceRecord(t, stateRoot, oldBin, newBin, configPath)
	if err := WriteProvenance(stateRoot, rec); err != nil {
		t.Fatal(err)
	}

	// Verify no secrets in the file (should only contain hashes and paths).
	data, _ := os.ReadFile(filepath.Join(stateRoot, provenanceFileName))
	if len(data) == 0 {
		t.Fatal("provenance file is empty")
	}
}

func TestProvenanceReplayRefused(t *testing.T) {
	stateRoot := t.TempDir()
	oldBin := writeFakeBinary(t, filepath.Join(stateRoot, "old-bin"), "old-binary")
	newBin := writeFakeBinary(t, filepath.Join(stateRoot, "new-bin"), "new-binary")
	configPath := filepath.Join(stateRoot, "config.yaml")

	rec := makeProvenanceRecord(t, stateRoot, oldBin, newBin, configPath)
	if err := WriteProvenance(stateRoot, rec); err != nil {
		t.Fatal(err)
	}

	// Consume it.
	if err := ConsumeProvenance(stateRoot); err != nil {
		t.Fatal(err)
	}

	// Re-read should return nil (no record).
	got, err := ReadProvenance(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatal("expected nil after consumption")
	}
}

func TestValidateProvenanceBackgroundDaemonRequiresIdentity(t *testing.T) {
	stateRoot := t.TempDir()
	oldBin := writeFakeBinary(t, filepath.Join(stateRoot, "old-bin"), "old-binary")
	newBin := writeFakeBinary(t, filepath.Join(stateRoot, "new-bin"), "new-binary")
	configPath := filepath.Join(stateRoot, "config.yaml")

	rec := makeProvenanceRecord(t, stateRoot, oldBin, newBin, configPath)
	// Remove background-daemon conditional identity.
	rec.BackgroundDaemonPID = 0
	rec.BackgroundDaemonExe = ""

	err := ValidateProvenance(&rec, ProvenanceValidation{
		InstalledBinaryPath: newBin,
		ConfigPath:          configPath,
	})
	if err == nil {
		t.Fatal("expected error for missing background-daemon identity")
	}
}

func TestValidateProvenanceLaunchdRequiresServiceDef(t *testing.T) {
	stateRoot := t.TempDir()
	oldBin := writeFakeBinary(t, filepath.Join(stateRoot, "old-bin"), "old-binary")
	newBin := writeFakeBinary(t, filepath.Join(stateRoot, "new-bin"), "new-binary")
	configPath := filepath.Join(stateRoot, "config.yaml")
	svcDef := filepath.Join(stateRoot, "agent.plist")
	svcData := []byte(`<plist version="1.0"><dict><key>Label</key><string>com.droid-proxy</string></dict></plist>`)
	os.WriteFile(svcDef, svcData, 0o600)

	rec := makeProvenanceRecord(t, stateRoot, oldBin, newBin, configPath)
	rec.ServiceKind = "launchd"
	rec.ServiceDefPath = svcDef
	rec.ServiceDefHash = sha256Hex(svcData)
	rec.BackgroundDaemonPID = 0
	rec.BackgroundDaemonExe = ""

	err := ValidateProvenance(&rec, ProvenanceValidation{
		InstalledBinaryPath: newBin,
		ConfigPath:          configPath,
	})
	if err != nil {
		t.Fatalf("expected valid launchd provenance with service def: %v", err)
	}
}

func TestValidateProvenanceLaunchdMissingServiceDefRefuses(t *testing.T) {
	stateRoot := t.TempDir()
	oldBin := writeFakeBinary(t, filepath.Join(stateRoot, "old-bin"), "old-binary")
	newBin := writeFakeBinary(t, filepath.Join(stateRoot, "new-bin"), "new-binary")
	configPath := filepath.Join(stateRoot, "config.yaml")

	rec := makeProvenanceRecord(t, stateRoot, oldBin, newBin, configPath)
	rec.ServiceKind = "launchd"
	// Missing service definition path and hash.
	rec.ServiceDefPath = ""
	rec.ServiceDefHash = ""
	rec.BackgroundDaemonPID = 0
	rec.BackgroundDaemonExe = ""

	err := ValidateProvenance(&rec, ProvenanceValidation{
		InstalledBinaryPath: newBin,
		ConfigPath:          configPath,
	})
	if err == nil {
		t.Fatal("expected error for launchd provenance without service definition")
	}
}

func TestValidateProvenanceServiceDefHashMismatchRefuses(t *testing.T) {
	stateRoot := t.TempDir()
	oldBin := writeFakeBinary(t, filepath.Join(stateRoot, "old-bin"), "old-binary")
	newBin := writeFakeBinary(t, filepath.Join(stateRoot, "new-bin"), "new-binary")
	configPath := filepath.Join(stateRoot, "config.yaml")
	svcDef := filepath.Join(stateRoot, "agent.plist")
	svcData := []byte(`<plist version="1.0"><dict></dict></plist>`)
	os.WriteFile(svcDef, svcData, 0o600)

	rec := makeProvenanceRecord(t, stateRoot, oldBin, newBin, configPath)
	rec.ServiceKind = "launchd"
	rec.ServiceDefPath = svcDef
	rec.ServiceDefHash = sha256Hex(svcData)
	rec.BackgroundDaemonPID = 0
	rec.BackgroundDaemonExe = ""

	// Edit the service definition after record creation.
	os.WriteFile(svcDef, []byte(`<plist version="1.0"><dict><key>changed</key></dict></plist>`), 0o600)

	err := ValidateProvenance(&rec, ProvenanceValidation{
		InstalledBinaryPath: newBin,
		ConfigPath:          configPath,
	})
	if err == nil {
		t.Fatal("expected error for changed service definition hash")
	}
}

func TestValidateProvenanceUnknownServiceKindRefuses(t *testing.T) {
	stateRoot := t.TempDir()
	oldBin := writeFakeBinary(t, filepath.Join(stateRoot, "old-bin"), "old-binary")
	newBin := writeFakeBinary(t, filepath.Join(stateRoot, "new-bin"), "new-binary")
	configPath := filepath.Join(stateRoot, "config.yaml")

	rec := makeProvenanceRecord(t, stateRoot, oldBin, newBin, configPath)
	rec.ServiceKind = "unknown-kind"

	err := ValidateProvenance(&rec, ProvenanceValidation{
		InstalledBinaryPath: newBin,
		ConfigPath:          configPath,
	})
	if err == nil {
		t.Fatal("expected error for unknown service kind")
	}
}
