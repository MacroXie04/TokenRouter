package relay

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateRecoveryJournalDirectoryRejectsBroadAndAmbiguousTargets(t *testing.T) {
	workingDirectory, err := os.Getwd()
	require.NoError(t, err)

	unsafeTargets := map[string]string{
		"empty":             "",
		"dot":               ".",
		"parent shorthand":  "..",
		"filesystem root":   string(filepath.Separator),
		"working directory": workingDirectory,
	}
	if parent := filepath.Dir(workingDirectory); parent != workingDirectory {
		unsafeTargets["working directory parent"] = parent
	}
	for name, target := range unsafeTargets {
		t.Run(name, func(t *testing.T) {
			_, err := validateRecoveryJournalDirectory(target, "test")
			assert.Error(t, err)
		})
	}

	root := t.TempDir()
	file := filepath.Join(root, "not-a-directory")
	require.NoError(t, os.WriteFile(file, []byte("sentinel"), 0o600))
	_, err = validateRecoveryJournalDirectory(file, "test")
	assert.Error(t, err)
	insecureDirectory := filepath.Join(root, "shared-directory")
	require.NoError(t, os.Mkdir(insecureDirectory, 0o755))
	require.NoError(t, os.Chmod(insecureDirectory, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(insecureDirectory, "unrelated"), []byte("sentinel"), 0o600))
	_, err = validateRecoveryJournalDirectory(insecureDirectory, "test")
	assert.Error(t, err, "an existing shared directory must not be chmodded as a side effect")

	if runtime.GOOS != "windows" {
		symlink := filepath.Join(root, "journal-link")
		require.NoError(t, os.Symlink(root, symlink))
		_, err = validateRecoveryJournalDirectory(symlink, "test")
		assert.Error(t, err)
	}
}

func TestValidateRecoveryJournalDirectoryAcceptsDedicatedLeaf(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "existing-journal")
	require.NoError(t, os.Mkdir(existing, 0o700))

	for name, target := range map[string]string{
		"existing":     existing,
		"not existing": filepath.Join(root, "new-journal"),
	} {
		t.Run(name, func(t *testing.T) {
			validated, err := validateRecoveryJournalDirectory(target, "test")
			require.NoError(t, err)
			expected, err := filepath.Abs(target)
			require.NoError(t, err)
			expected, err = resolveRecoveryJournalDirectoryPath(expected)
			require.NoError(t, err)
			assert.Equal(t, expected, validated)
		})
	}

	if runtime.GOOS != "windows" {
		t.Run("resolves existing ancestor symlinks", func(t *testing.T) {
			actualParent := filepath.Join(root, "actual-parent")
			require.NoError(t, os.Mkdir(actualParent, 0o700))
			linkedParent := filepath.Join(root, "linked-parent")
			require.NoError(t, os.Symlink(actualParent, linkedParent))
			validated, err := validateRecoveryJournalDirectory(
				filepath.Join(linkedParent, "journal"), "test",
			)
			require.NoError(t, err)
			resolvedParent, err := filepath.EvalSymlinks(actualParent)
			require.NoError(t, err)
			assert.Equal(t, filepath.Join(resolvedParent, "journal"), validated)
		})
	}
}

func TestEveryRecoveryJournalFamilyRejectsFilesystemRoot(t *testing.T) {
	tests := []struct {
		name     string
		env      string
		validate func() (string, error)
	}{
		{name: "video", env: "VIDEO_TASK_RECOVERY_DIR", validate: validatedVideoRecoveryJournalDirectory},
		{name: "Jimeng", env: "JIMENG_RECOVERY_DIR", validate: validatedJimengRecoveryDirectory},
		{name: "Kling", env: "KLING_TASK_RECOVERY_DIR", validate: validatedKlingRecoveryJournalDirectory},
		{name: "Doubao", env: "DOUBAO_TASK_RECOVERY_DIR", validate: validatedDoubaoRecoveryJournalDirectory},
		{name: "Suno", env: "SUNO_TASK_RECOVERY_DIR", validate: validatedSunoRecoveryJournalDirectory},
		{name: "Midjourney", env: "MIDJOURNEY_TASK_RECOVERY_DIR", validate: validatedMidjourneyRecoveryJournalDirectory},
		{name: "Hailuo", env: "HAILUO_TASK_RECOVERY_DIR", validate: validatedHailuoRecoveryJournalDirectory},
		{name: "Vidu", env: "VIDU_TASK_RECOVERY_DIR", validate: validatedViduRecoveryJournalDirectory},
		{name: "AliWan", env: "ALI_VIDEO_TASK_RECOVERY_DIR", validate: validatedAliWanRecoveryJournalDirectory},
		{name: "GeminiVeo", env: "GEMINI_VEO_TASK_RECOVERY_DIR", validate: validatedGeminiVeoRecoveryJournalDirectory},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(test.env, string(filepath.Separator))
			_, err := test.validate()
			assert.Error(t, err)
		})
	}

	taskID := "task_" + strings.Repeat("a", 32)
	t.Setenv("VIDEO_TASK_RECOVERY_DIR", string(filepath.Separator))
	_, err := videoRecoveryJournalPath(taskID)
	assert.Error(t, err)
	t.Setenv("JIMENG_RECOVERY_DIR", string(filepath.Separator))
	_, err = jimengRecoveryPath(taskID)
	assert.Error(t, err)
	t.Setenv("KLING_TASK_RECOVERY_DIR", string(filepath.Separator))
	_, err = klingRecoveryJournalPath(taskID)
	assert.Error(t, err)
	t.Setenv("DOUBAO_TASK_RECOVERY_DIR", string(filepath.Separator))
	_, err = doubaoRecoveryJournalPath(taskID)
	assert.Error(t, err)
}

func TestLoadJimengRecoveryRejectsJournalSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges on Windows")
	}
	directory := t.TempDir()
	require.NoError(t, os.Chmod(directory, 0o700))
	t.Setenv("JIMENG_RECOVERY_DIR", directory)
	taskID := "task_" + strings.Repeat("a", 32)
	target := filepath.Join(directory, "unrelated.json")
	require.NoError(t, os.WriteFile(target, []byte(`{"status":"submitted"}`), 0o600))
	path, err := jimengRecoveryPath(taskID)
	require.NoError(t, err)
	require.NoError(t, os.Symlink(target, path))

	_, err = loadJimengRecovery(taskID)
	assert.Error(t, err)
}
