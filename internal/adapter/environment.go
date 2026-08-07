package adapter

import (
	"crypto/sha256"
	"encoding/hex"
)

// EnvironmentRegistrations preserves the original explicit companion-adapter
// overrides without restoring ambient PATH discovery. The negotiated claims
// are checked against these bounded compatibility claims before indexing.
func EnvironmentRegistrations(getenv func(string) string) ([]Registration, error) {
	type known struct {
		environment string
		name        string
		claims      Claims
		permissions Permissions
	}
	values := []known{
		{
			environment: "WEAVE_DOTNET_ADAPTER", name: "weave-dotnet",
			claims: Claims{Inputs: Inputs{
				Extensions: []string{".cs", ".csproj", ".csx", ".fs", ".fsproj", ".fsx", ".props", ".sln", ".slnx", ".targets"},
				Filenames:  []string{"directory.build.props", "directory.build.targets", "directory.packages.props", "global.json", "nuget.config", "packages.lock.json"},
			}, Evidence: []string{"declared", "exact"}},
			permissions: Permissions{BuildTool: true},
		},
		{
			environment: "WEAVE_PYTHON_ADAPTER", name: "weave-python",
			claims: Claims{Inputs: Inputs{Extensions: []string{".py"}, Filenames: []string{"pyproject.toml"}}, Evidence: []string{"declared", "exact", "syntactic"}},
		},
		{
			environment: "WEAVE_RUST_ADAPTER", name: "weave-rust",
			claims: Claims{Inputs: Inputs{
				Extensions:     []string{".rs"},
				Filenames:      []string{"cargo.lock", "cargo.toml", "rust-project.json", "rust-toolchain", "rust-toolchain.toml"},
				ProjectMarkers: []string{"cargo.toml", "rust-project.json"},
			}, Evidence: []string{"exact"}, InvalidationAllFiles: true},
			permissions: Permissions{BuildTool: true},
		},
		{
			environment: "WEAVE_CPP_ADAPTER", name: "scip:scip-clang",
			claims: Claims{Inputs: Inputs{
				Extensions: []string{".c", ".cc", ".cpp", ".cxx", ".cu", ".h", ".hh", ".hpp"},
				Filenames:  []string{"compile_commands.json"}, ProjectMarkers: []string{"compile_commands.json"},
			}, Evidence: []string{"exact"}, InvalidationAllFiles: true},
			permissions: Permissions{BuildTool: true},
		},
		{
			environment: "WEAVE_TYPESCRIPT_ADAPTER", name: "scip:scip-typescript",
			claims: Claims{Inputs: Inputs{
				Extensions: []string{".js", ".jsx", ".ts", ".tsx"},
				Filenames:  []string{"jsconfig.json", "package.json", "tsconfig.json"}, ProjectMarkers: []string{"jsconfig.json", "tsconfig.json"},
			}, Evidence: []string{"exact"}, InvalidationAllFiles: true},
		},
	}
	result := make([]Registration, 0, len(values))
	for _, value := range values {
		path := getenv(value.environment)
		if path == "" {
			continue
		}
		claims, err := NormalizeClaims(value.claims)
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256([]byte(value.environment + "\x00" + path))
		result = append(result, Registration{
			Name: value.name, Command: []string{path}, Inputs: claims.Inputs, Claims: claims,
			Permissions: value.permissions, ConfigFingerprint: "sha256:" + hex.EncodeToString(digest[:]),
			Source: "environment",
		})
	}
	return result, nil
}
