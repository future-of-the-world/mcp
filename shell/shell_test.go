// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package shell

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.amidman.dev/mcp/decode"
)

// TestDecodeConnect_Defaults pins the empty-connect behavior: missing
// optional fields leave the config struct at their zero values and the
// missing-required working_dir surfaces at validate(), not decode().
func TestDecodeConnect_Defaults(t *testing.T) {
	t.Parallel()

	cfg, err := decodeConnect(make(map[string]any))
	require.NoError(t, err)

	assert.Empty(t, cfg.WorkingDir)
	assert.Equal(t, Duration(0), cfg.Timeout)
	assert.Equal(t, 0, cfg.MaxOutputBytes)
	assert.Empty(t, cfg.Shell)
	assert.Empty(t, cfg.Flags)
	assert.Empty(t, cfg.Env)
}

// TestDecodeConnect_Full populates every field and verifies the values
// flow through the YAML/JSON-friendly types intact.
func TestDecodeConnect_Full(t *testing.T) {
	t.Parallel()

	cfg, err := decodeConnect(map[string]any{
		"working_dir":      "/tmp",
		"timeout":          "45s",
		"max_output_bytes": 524288,
		"shell":            "/bin/bash",
		"shell_flags":      []string{"-lic"},
		"env": map[string]any{
			"LANG": "C.UTF-8",
			"PATH": "/usr/bin:/bin",
		},
	})
	require.NoError(t, err)

	assert.Equal(t, "/tmp", cfg.WorkingDir)
	assert.Equal(t, 45*time.Second, cfg.Timeout.Duration())
	assert.Equal(t, 524288, cfg.MaxOutputBytes)
	assert.Equal(t, "/bin/bash", cfg.Shell)
	assert.Equal(t, []string{"-lic"}, cfg.Flags)
	assert.Equal(t, map[string]string{
		"LANG": "C.UTF-8",
		"PATH": "/usr/bin:/bin",
	}, cfg.Env)
}

// TestDecodeConnect_NumericTimeout verifies the decode.AsString coercion
// path: a numeric YAML value (e.g. timeout: 30) is stringified to "30"
// and then rejected by time.ParseDuration as "missing unit". This is the
// same behavior as websearch's timeout field and surfaces genuine
// config bugs as clear errors.
func TestDecodeConnect_NumericTimeout(t *testing.T) {
	t.Parallel()

	_, err := decodeConnect(map[string]any{
		"working_dir": "/tmp",
		"timeout":     30,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing unit")
}

// TestDecodeConnect_NonScalarTimeout verifies the strict path: a
// non-scalar value (a map) where a string is expected produces a
// wrapped decode.ErrWrongType.
func TestDecodeConnect_NonScalarTimeout(t *testing.T) {
	t.Parallel()

	_, err := decodeConnect(map[string]any{
		"working_dir": "/tmp",
		"timeout":     map[string]any{"seconds": 5},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, decode.ErrWrongType)
}

// TestDecodeConnect_InvalidTimeoutFormat verifies that a non-parseable
// duration string is rejected at decode time with the time.ParseDuration
// error wrapped.
func TestDecodeConnect_InvalidTimeoutFormat(t *testing.T) {
	t.Parallel()

	_, err := decodeConnect(map[string]any{
		"working_dir": "/tmp",
		"timeout":     "not-a-duration",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid duration")
}

// TestDecodeConnect_MaxOutputBytesTypes verifies that integer, int64,
// and float64 (YAML's natural number type) are all accepted; other
// types (string, map) are rejected.
func TestDecodeConnect_MaxOutputBytesTypes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     any
		want    int
		wantErr bool
	}{
		{"int", 1024, 1024, false},
		{"int64", int64(2048), 2048, false},
		{"float64", float64(4096), 4096, false},
		{"string", "8192", 0, true},
		{"map", map[string]any{"bytes": 100}, 0, true},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := decodeConnect(map[string]any{
				"working_dir":      "/tmp",
				"max_output_bytes": testCase.raw,
			})
			if testCase.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "must be an integer")

				return
			}

			require.NoError(t, err)
			assert.Equal(t, testCase.want, cfg.MaxOutputBytes)
		})
	}
}

// TestDecodeConnect_EnvWrongType verifies that a non-map value where
// connect.env expects a map produces a wrapped errEnvWrongType.
func TestDecodeConnect_EnvWrongType(t *testing.T) {
	t.Parallel()

	_, err := decodeConnect(map[string]any{
		"working_dir": "/tmp",
		"env":         "LANG=C.UTF-8",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, errEnvWrongType)
}

// TestDecodeConnect_ShellFlags_Default verifies that omitting
// connect.shell_flags leaves cfg.Flags at its zero value (empty slice).
// The handler applies the default `["-c"]` at buildRunOpts time; this
// test pins the decode-time behavior so future changes don't accidentally
// pre-fill a non-empty default during decode.
func TestDecodeConnect_ShellFlags_Default(t *testing.T) {
	t.Parallel()

	cfg, err := decodeConnect(map[string]any{
		"working_dir": "/tmp",
	})
	require.NoError(t, err)
	assert.Empty(t, cfg.Flags)
}

// TestDecodeConnect_ShellFlags_Empty verifies that an explicit
// `shell_flags: []` is preserved as an empty slice (the handler's
// default kicks in there, not in decode). This avoids silently
// overwriting an operator's intentional empty-list config with
// a non-empty default.
func TestDecodeConnect_ShellFlags_Empty(t *testing.T) {
	t.Parallel()

	cfg, err := decodeConnect(map[string]any{
		"working_dir": "/tmp",
		"shell_flags": []string{},
	})
	require.NoError(t, err)
	assert.NotNil(t, cfg.Flags)
	assert.Empty(t, cfg.Flags)
}

// TestDecodeConnect_ShellFlags_WrongType verifies that a non-list value
// (e.g. a string) where connect.shell_flags expects a list surfaces
// as a clear decode error rather than a silent misconfig.
func TestDecodeConnect_ShellFlags_WrongType(t *testing.T) {
	t.Parallel()

	_, err := decodeConnect(map[string]any{
		"working_dir": "/tmp",
		"shell_flags": "-lic",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "shell_flags")
	assert.Contains(t, err.Error(), "list of strings")
}

// TestDecodeConnect_ShellFlags_NonStringElement verifies that a list
// element that isn't a scalar (e.g. a map) surfaces as a per-element
// decode error.
func TestDecodeConnect_ShellFlags_NonStringElement(t *testing.T) {
	t.Parallel()

	_, err := decodeConnect(map[string]any{
		"working_dir": "/tmp",
		"shell_flags": []any{
			"-l",
			map[string]any{"nested": "value"},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "shell_flags[1]")
}

// TestDecodeConnect_ShellFlags_NumericElement verifies that numeric
// list elements are accepted via decode.AsString coercion. This mirrors
// the timeout/max_output_bytes numeric-coercion behavior.
func TestDecodeConnect_ShellFlags_NumericElement(t *testing.T) {
	t.Parallel()

	cfg, err := decodeConnect(map[string]any{
		"working_dir": "/tmp",
		"shell_flags": []any{"-c", 1},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"-c", "1"}, cfg.Flags)
}

// TestDecodeConnect_EnvNonScalarValue verifies that a map value that is
// not a scalar (e.g. a nested map or slice) surfaces as a wrapped
// decode error per the env map's per-value coercion.
func TestDecodeConnect_EnvNonScalarValue(t *testing.T) {
	t.Parallel()

	_, err := decodeConnect(map[string]any{
		"working_dir": "/tmp",
		"env": map[string]any{
			"PATH": map[string]any{"nested": "value"},
		},
	})
	require.Error(t, err)
}

// TestValidate_MissingWorkingDir verifies the required-field check
// surfaces as a clear error at validate time (not decode time).
func TestValidate_MissingWorkingDir(t *testing.T) {
	t.Parallel()

	cfg := config{}
	err := cfg.validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, errWorkingDirEmpty)
}

// TestValidate_NegativeTimeout verifies the negative-timeout guard.
func TestValidate_NegativeTimeout(t *testing.T) {
	t.Parallel()

	cfg := config{ //nolint:exhaustruct // optional fields are deliberately left zero
		WorkingDir: "/tmp",
		Timeout:    -1,
	}
	err := cfg.validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, errTimeoutNegative)
}

// TestValidate_NegativeMaxBytes verifies the negative-cap guard.
func TestValidate_NegativeMaxBytes(t *testing.T) {
	t.Parallel()

	cfg := config{ //nolint:exhaustruct // optional fields are deliberately left zero
		WorkingDir:     "/tmp",
		MaxOutputBytes: -1,
	}
	err := cfg.validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, errMaxBytesNonPos)
}

// TestValidate_OK verifies the happy path: all required fields present,
// no invalid optional values.
func TestValidate_OK(t *testing.T) {
	t.Parallel()

	cfg := config{ //nolint:exhaustruct // partial literal is intentional
		WorkingDir:     "/tmp",
		Timeout:        Duration(30 * time.Second),
		MaxOutputBytes: 1024,
		Shell:          "/bin/sh",
	}
	assert.NoError(t, cfg.validate())
}

// TestBuildRunOpts_MergesEnv verifies that per-call env overrides win
// on key collision with connect.env. Merging happens in handler.go's
// buildRunOpts, not in decodeConnect.
func TestBuildRunOpts_MergesEnv(t *testing.T) {
	t.Parallel()

	cfg := &config{ //nolint:exhaustruct // partial literal is intentional
		WorkingDir: "/tmp",
		Env: map[string]string{
			"LANG": "C.UTF-8",
			"PATH": "/usr/bin:/bin",
		},
	}

	args := runCommandArgs{ //nolint:exhaustruct // Directory is a fixed absolute path inside /tmp
		Command:   "echo hi",
		Directory: "/tmp",
		Env: map[string]string{
			"PATH": "/custom/bin",
			"FOO":  "bar",
		},
	}

	root, canon := openTestRoot(t, cfg.WorkingDir)
	opts, err := buildRunOpts(cfg, args, root, canon)
	require.NoError(t, err)

	assert.Equalf(t, "/custom/bin", opts.Env["PATH"], "per-call PATH must win")
	assert.Equalf(t, "C.UTF-8", opts.Env["LANG"], "untouched base env vars must be preserved")
	assert.Equalf(t, "bar", opts.Env["FOO"], "new per-call vars must be added")
}

// TestBuildRunOpts_FlagsDefault pins the buildRunOpts-level default:
// an empty cfg.Flags becomes `["-c"]` so downstream runShell always
// sees a usable flag list. The safety-net in runShell handles the
// edge case where someone calls runShell directly with no flags.
func TestBuildRunOpts_FlagsDefault(t *testing.T) {
	t.Parallel()

	cfg := &config{ //nolint:exhaustruct // Flags intentionally left empty
		WorkingDir: "/tmp",
	}

	root, canon := openTestRoot(t, cfg.WorkingDir)
	opts, err := buildRunOpts(
		cfg,
		runCommandArgs{ //nolint:exhaustruct // only Command and Directory are exercised
			Command:   "echo hi",
			Directory: "/tmp",
		},
		root,
		canon,
	)
	require.NoError(t, err)

	assert.Equal(t, []string{"-c"}, opts.Flags)
}

// TestBuildRunOpts_FlagsPassedThrough verifies that a non-empty
// cfg.Flags is preserved verbatim by buildRunOpts. Operators rely on
// this to compose `["-lic"]` (zsh rc sourcing), `["-l"]` (bash
// login), `["--noprofile", "-c"]`, etc.
func TestBuildRunOpts_FlagsPassedThrough(t *testing.T) {
	t.Parallel()

	cfg := &config{ //nolint:exhaustruct // Env defaults to nil which is empty
		WorkingDir: "/tmp",
		Shell:      "/bin/zsh",
		Flags:      []string{"-lic"},
	}

	args := runCommandArgs{ //nolint:exhaustruct // only Command and Directory are exercised
		Command:   "echo hi",
		Directory: "/tmp",
	}
	root, canon := openTestRoot(t, cfg.WorkingDir)
	opts, err := buildRunOpts(cfg, args, root, canon)
	require.NoError(t, err)

	assert.Equal(t, []string{"-lic"}, opts.Flags)
}

// TestBuildRunOpts_InvalidTimeout verifies that a non-parseable per-call
// timeout surfaces as a clear error before runShell is invoked.
func TestBuildRunOpts_InvalidTimeout(t *testing.T) {
	t.Parallel()

	cfg := &config{WorkingDir: "/tmp"} //nolint:exhaustruct // only WorkingDir matters here
	args := runCommandArgs{            //nolint:exhaustruct // Command, Directory, Timeout exercised
		Command:   "echo hi",
		Directory: "/tmp",
		Timeout:   "not-a-duration",
	}

	root, canon := openTestRoot(t, cfg.WorkingDir)
	_, err := buildRunOpts(cfg, args, root, canon)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid timeout")
}

// TestEnvMapToSlice_PreservesKeys verifies the env slice helper
// produces one "key=value" entry per map entry. Order is non-deterministic
// by Go map iteration semantics; the test checks contents, not order.
func TestEnvMapToSlice_PreservesKeys(t *testing.T) {
	t.Parallel()

	env := map[string]string{"A": "1", "B": "2"}
	out := envMapToSlice(env)

	assert.Len(t, out, 2)

	for _, kv := range out {
		assert.Contains(t, []string{"A=1", "B=2"}, kv)
	}
}

// TestEnvMapToSlice_Empty verifies the empty-map path returns an empty
// (non-nil) slice so exec.Cmd.Env stays unset and the child gets no
// inherited environment — see runShell for the env-pass-through policy.
func TestEnvMapToSlice_Empty(t *testing.T) {
	t.Parallel()

	out := envMapToSlice(map[string]string(nil))
	assert.Empty(t, out)
}
