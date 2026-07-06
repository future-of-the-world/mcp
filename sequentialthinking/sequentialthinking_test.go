// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package sequentialthinking

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- decodeConnect ---

func TestDecodeConnect_DefaultsWhenAbsent(t *testing.T) {
	t.Parallel()

	cfg, err := decodeConnect(make(map[string]any))
	require.NoError(t, err)
	assert.False(t, cfg.DisableThoughtLogging)
}

func TestDecodeConnect_DefaultsWhenNil(t *testing.T) {
	t.Parallel()

	cfg, err := decodeConnect(map[string]any(nil))
	require.NoError(t, err)
	assert.False(t, cfg.DisableThoughtLogging)
}

func TestDecodeConnect_AcceptsBoolTrue(t *testing.T) {
	t.Parallel()

	cfg, err := decodeConnect(map[string]any{
		connectKeyDisableThoughtLogging: true,
	})
	require.NoError(t, err)
	assert.True(t, cfg.DisableThoughtLogging)
}

func TestDecodeConnect_AcceptsBoolFalse(t *testing.T) {
	t.Parallel()

	cfg, err := decodeConnect(map[string]any{
		connectKeyDisableThoughtLogging: false,
	})
	require.NoError(t, err)
	assert.False(t, cfg.DisableThoughtLogging)
}

func TestDecodeConnect_AcceptsStringTrue(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"true", "True", "TRUE"} {
		cfg, err := decodeConnect(map[string]any{
			connectKeyDisableThoughtLogging: value,
		})
		require.NoErrorf(t, err, "input %q", value)
		assert.Truef(t, cfg.DisableThoughtLogging, "input %q", value)
	}
}

func TestDecodeConnect_AcceptsStringFalse(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"false", "False", "FALSE"} {
		cfg, err := decodeConnect(map[string]any{
			connectKeyDisableThoughtLogging: value,
		})
		require.NoErrorf(t, err, "input %q", value)
		assert.Falsef(t, cfg.DisableThoughtLogging, "input %q", value)
	}
}

func TestDecodeConnect_AcceptsIntegers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input any
		want  bool
	}{
		{int(1), true},
		{int(0), false},
		{int64(42), true},
		{int64(0), false},
		{float64(7), true},
		{float64(0), false},
	}
	for _, testCase := range tests {
		cfg, err := decodeConnect(map[string]any{
			connectKeyDisableThoughtLogging: testCase.input,
		})
		require.NoErrorf(t, err, "input %v", testCase.input)
		assert.Equalf(t, testCase.want, cfg.DisableThoughtLogging, "input %v", testCase.input)
	}
}

func TestDecodeConnect_RejectsGarbageString(t *testing.T) {
	t.Parallel()

	_, err := decodeConnect(map[string]any{
		connectKeyDisableThoughtLogging: "not-a-bool",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), connectKeyDisableThoughtLogging)
}

func TestDecodeConnect_RejectsMapValue(t *testing.T) {
	t.Parallel()

	_, err := decodeConnect(map[string]any{
		connectKeyDisableThoughtLogging: map[string]any{"nested": "value"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), connectKeyDisableThoughtLogging)
}

// --- decodeBool (the primitive decoder behind decodeConnect) ---

func TestDecodeBool_RejectsSlice(t *testing.T) {
	t.Parallel()

	_, err := decodeBool([]string{"x"})
	require.Error(t, err)
}

// --- newSequentialThinkingServer ---

func TestNewServer_HasEmptyState(t *testing.T) {
	t.Parallel()

	server := newSequentialThinkingServer(config{}, slog.New(slog.DiscardHandler))

	assert.Empty(t, server.thoughtHistory)
	assert.Empty(t, server.branches)
	assert.False(t, server.disableThoughtLogging)
}

func TestNewServer_HonorsConfig(t *testing.T) {
	t.Parallel()

	server := newSequentialThinkingServer(
		config{DisableThoughtLogging: true},
		slog.New(slog.DiscardHandler),
	)
	assert.True(t, server.disableThoughtLogging)
}

// --- processThought ---

func TestProcessThought_RegularThoughtAppends(t *testing.T) {
	t.Parallel()

	server := newSequentialThinkingServer(config{}, slog.New(slog.DiscardHandler))

	resp := server.processThought(
		t.Context(),
		&ThoughtData{ //nolint:exhaustruct // defaults are intentional
			Thought:           "first",
			ThoughtNumber:     1,
			TotalThoughts:     3,
			NextThoughtNeeded: true,
		},
	)

	assert.Equal(t, 1, resp.ThoughtNumber)
	assert.Equal(t, 3, resp.TotalThoughts)
	assert.True(t, resp.NextThoughtNeeded)
	assert.Equal(t, []string{}, resp.Branches)
	assert.Equal(t, 1, resp.ThoughtHistoryLength)
	assert.Len(t, server.thoughtHistory, 1)
}

func TestProcessThought_BumpsTotalWhenNumberExceeds(t *testing.T) {
	t.Parallel()

	server := newSequentialThinkingServer(config{}, slog.New(slog.DiscardHandler))

	resp := server.processThought(
		t.Context(),
		&ThoughtData{ //nolint:exhaustruct // defaults are intentional
			Thought:           "expansion",
			ThoughtNumber:     10,
			TotalThoughts:     3,
			NextThoughtNeeded: false,
		},
	)

	assert.Equalf(t, 10, resp.TotalThoughts, "total should be bumped up to thought_number")
}

func TestProcessThought_RevisionDoesNotCreateBranch(t *testing.T) {
	t.Parallel()

	server := newSequentialThinkingServer(config{}, slog.New(slog.DiscardHandler))

	resp := server.processThought(
		t.Context(),
		&ThoughtData{ //nolint:exhaustruct // defaults are intentional
			Thought:           "revised",
			ThoughtNumber:     2,
			TotalThoughts:     3,
			IsRevision:        true,
			RevisesThought:    1,
			NextThoughtNeeded: true,
		},
	)

	assert.Equal(t, []string{}, resp.Branches)
	assert.Emptyf(t, server.branches, "revisions don't create branches")
	assert.Len(t, server.thoughtHistory, 1)
}

func TestProcessThought_BranchRecorded(t *testing.T) {
	t.Parallel()

	server := newSequentialThinkingServer(config{}, slog.New(slog.DiscardHandler))

	resp := server.processThought(
		t.Context(),
		&ThoughtData{ //nolint:exhaustruct // defaults are intentional
			Thought:           "branch a step 1",
			ThoughtNumber:     4,
			TotalThoughts:     5,
			BranchFromThought: 2,
			BranchID:          "alt",
			NextThoughtNeeded: true,
		},
	)

	assert.Equal(t, []string{"alt"}, resp.Branches)
	assert.Len(t, server.branches["alt"], 1)
}

func TestProcessThought_MultipleBranchesSorted(t *testing.T) {
	t.Parallel()

	server := newSequentialThinkingServer(config{}, slog.New(slog.DiscardHandler))

	for _, branchID := range []string{"zebra", "alpha", "mike"} {
		server.processThought(
			t.Context(),
			&ThoughtData{ //nolint:exhaustruct // defaults are intentional
				Thought:           branchID,
				ThoughtNumber:     1,
				TotalThoughts:     1,
				BranchFromThought: 1,
				BranchID:          branchID,
				NextThoughtNeeded: false,
			},
		)
	}

	got := server.processThought(
		t.Context(),
		&ThoughtData{ //nolint:exhaustruct // defaults are intentional
			Thought:           "extra",
			ThoughtNumber:     2,
			TotalThoughts:     2,
			NextThoughtNeeded: false,
		},
	).Branches
	assert.Equal(t, []string{"alpha", "mike", "zebra"}, got)
}

func TestProcessThought_LogsWhenEnabled(t *testing.T) {
	t.Parallel()

	var buf strings.Builder

	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	server := newSequentialThinkingServer(config{DisableThoughtLogging: false}, logger)

	server.processThought(
		t.Context(),
		&ThoughtData{ //nolint:exhaustruct // defaults are intentional
			Thought:           "logged",
			ThoughtNumber:     1,
			TotalThoughts:     1,
			NextThoughtNeeded: false,
		},
	)

	assert.Contains(t, buf.String(), "sequentialthinking recorded")
	assert.Contains(t, buf.String(), "thought_number=1")
}

func TestProcessThought_SilentWhenDisabled(t *testing.T) {
	t.Parallel()

	var buf strings.Builder

	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	server := newSequentialThinkingServer(config{DisableThoughtLogging: true}, logger)

	server.processThought(
		t.Context(),
		&ThoughtData{ //nolint:exhaustruct // defaults are intentional
			Thought:           "quiet",
			ThoughtNumber:     1,
			TotalThoughts:     1,
			NextThoughtNeeded: false,
		},
	)

	assert.Empty(t, buf.String())
}

// --- branchKeys ---

func TestBranchKeys_EmptyMap(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{}, branchKeys(map[string][]ThoughtData(nil)))
	assert.Equal(t, []string{}, branchKeys(make(map[string][]ThoughtData)))
}

func TestBranchKeys_Sorted(t *testing.T) {
	t.Parallel()

	got := branchKeys(map[string][]ThoughtData{
		"c": {},
		"a": {},
		"b": {},
	})
	assert.Equal(t, []string{"a", "b", "c"}, got)
}

// --- sortStrings ---

func TestSortStrings(t *testing.T) {
	t.Parallel()

	values := []string{"zebra", "alpha", "mike", "bravo"}
	sortStrings(values)
	assert.Equal(t, []string{"alpha", "bravo", "mike", "zebra"}, values)
}

func TestSortStrings_HandlesEmptyAndSingleton(t *testing.T) {
	t.Parallel()

	t.Run("empty", func(t *testing.T) {
		t.Parallel()

		values := []string{}
		sortStrings(values)
		assert.Empty(t, values)
	})

	t.Run("singleton", func(t *testing.T) {
		t.Parallel()

		values := []string{"only"}
		sortStrings(values)
		assert.Equal(t, []string{"only"}, values)
	})
}
