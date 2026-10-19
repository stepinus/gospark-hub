package dockerx

import (
	"os"
	"path/filepath"
	"strings"

	"gospark-hub/internal/store"
)

// EnsureRuntimeEnv creates the recipe's .env from .env.example on first run,
// filling the known secret placeholders with fresh tokens. It returns the path
// and whether the file was created now. A recipe without an .env.example has
// no runtime env at all (path "", created false).
func EnsureRuntimeEnv(recipeDir string) (path string, created bool, err error) {
	envFile := filepath.Join(recipeDir, ".env")
	if fileExists(envFile) {
		return envFile, false, nil
	}
	templateFile := filepath.Join(recipeDir, ".env.example")
	if !fileExists(templateFile) {
		return "", false, nil
	}
	data, err := os.ReadFile(templateFile)
	if err != nil {
		return "", false, err
	}
	rendered, err := renderRuntimeEnv(string(data))
	if err != nil {
		return "", false, err
	}
	if err := os.WriteFile(envFile, []byte(rendered), 0o600); err != nil {
		return "", false, err
	}
	// WriteFile honours umask only for new files; force 600 explicitly, the
	// file holds database and MinIO secrets.
	_ = os.Chmod(envFile, 0o600)
	return envFile, true, nil
}

func renderRuntimeEnv(templateText string) (string, error) {
	sharedMinio, err := store.NewRandomToken(24)
	if err != nil {
		return "", err
	}
	generated := map[string]string{}
	for key, n := range map[string]int{
		"USER_AUTH_SECRET":          48,
		"POSTGRES_PASSWORD":         24,
		"OPENSEARCH_ADMIN_PASSWORD": 24,
		"MINIO_ROOT_PASSWORD":       24,
		"S3_AWS_SECRET_ACCESS_KEY":  24,
	} {
		if key == "MINIO_ROOT_PASSWORD" || key == "S3_AWS_SECRET_ACCESS_KEY" {
			generated[key] = sharedMinio
			continue
		}
		token, err := store.NewRandomToken(n)
		if err != nil {
			return "", err
		}
		generated[key] = token
	}

	var out strings.Builder
	for _, line := range strings.Split(templateText, "\n") {
		if line == "" || strings.HasPrefix(strings.TrimLeft(line, " \t"), "#") || !strings.Contains(line, "=") {
			out.WriteString(line)
			out.WriteByte('\n')
			continue
		}
		key, value, _ := strings.Cut(line, "=")
		if v, ok := generated[key]; ok && strings.TrimSpace(value) == "" {
			value = v
		}
		out.WriteString(key + "=" + value + "\n")
	}
	return out.String(), nil
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}
