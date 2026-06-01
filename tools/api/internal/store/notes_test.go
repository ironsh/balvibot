package store_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ironsh/balvibot/tools/api/internal/store"
)

func TestNotesCreateAndList(t *testing.T) {
	st, ctx := openTestStore(t)
	require.NoError(t, st.EnsureGrantee(ctx, store.Grantee{GranteeID: "acme"}))

	// A plain note defaults to kind "note" and gets an id + created_at.
	first, err := st.CreateNote(ctx, store.Note{GranteeID: "acme", Content: "likes async updates", SignalNumber: "+15551234567"})
	require.NoError(t, err)
	require.NotZero(t, first.ID)
	require.Equal(t, store.NoteKindNote, first.Kind)
	require.Equal(t, "+15551234567", first.SignalNumber)
	require.False(t, first.CreatedAt.IsZero())

	// A typed note.
	pref, err := st.CreateNote(ctx, store.Note{GranteeID: "acme", Kind: store.NoteKindPreference, Content: "prefers email over calls"})
	require.NoError(t, err)

	// List returns newest first.
	notes, next, err := st.ListNotes(ctx, store.NoteFilter{GranteeID: "acme"})
	require.NoError(t, err)
	require.Zero(t, next)
	require.Len(t, notes, 2)
	require.Equal(t, pref.ID, notes[0].ID)
	require.Equal(t, first.ID, notes[1].ID)

	// Kind filter narrows results.
	onlyPrefs, _, err := st.ListNotes(ctx, store.NoteFilter{GranteeID: "acme", Kind: store.NoteKindPreference})
	require.NoError(t, err)
	require.Len(t, onlyPrefs, 1)
	require.Equal(t, pref.ID, onlyPrefs[0].ID)

	// An invalid kind is rejected on both write and read paths.
	_, err = st.CreateNote(ctx, store.Note{GranteeID: "acme", Kind: "bogus", Content: "x"})
	require.Error(t, err)
	_, _, err = st.ListNotes(ctx, store.NoteFilter{GranteeID: "acme", Kind: "bogus"})
	require.Error(t, err)
}

func TestNotesSupersede(t *testing.T) {
	st, ctx := openTestStore(t)
	require.NoError(t, st.EnsureGrantee(ctx, store.Grantee{GranteeID: "acme"}))

	old, err := st.CreateNote(ctx, store.Note{GranteeID: "acme", Kind: store.NoteKindStatus, Content: "in diligence"})
	require.NoError(t, err)

	newer, err := st.CreateNote(ctx, store.Note{GranteeID: "acme", Kind: store.NoteKindStatus, Content: "funded", SupersedesID: &old.ID})
	require.NoError(t, err)
	require.NotNil(t, newer.SupersedesID)
	require.Equal(t, old.ID, *newer.SupersedesID)

	// By default the superseded note is hidden; only the newer one shows.
	current, _, err := st.ListNotes(ctx, store.NoteFilter{GranteeID: "acme"})
	require.NoError(t, err)
	require.Len(t, current, 1)
	require.Equal(t, newer.ID, current[0].ID)

	// With include_superseded the old note reappears, annotated with what
	// superseded it.
	all, _, err := st.ListNotes(ctx, store.NoteFilter{GranteeID: "acme", IncludeSuperseded: true})
	require.NoError(t, err)
	require.Len(t, all, 2)
	var oldView *store.Note
	for i := range all {
		if all[i].ID == old.ID {
			oldView = &all[i]
		}
	}
	require.NotNil(t, oldView)
	require.NotNil(t, oldView.SupersededByID)
	require.Equal(t, newer.ID, *oldView.SupersededByID)
}

func TestNotesSinceFilterAndPagination(t *testing.T) {
	st, ctx := openTestStore(t)
	require.NoError(t, st.EnsureGrantee(ctx, store.Grantee{GranteeID: "acme"}))

	base := time.Unix(1_700_000_000, 0).UTC()
	for i := 0; i < 5; i++ {
		_, err := st.CreateNote(ctx, store.Note{
			GranteeID: "acme", Content: "n", CreatedAt: base.Add(time.Duration(i) * time.Hour),
		})
		require.NoError(t, err)
	}

	// since excludes notes created before the bound.
	cutoff := base.Add(2 * time.Hour)
	recent, _, err := st.ListNotes(ctx, store.NoteFilter{GranteeID: "acme", Since: &cutoff})
	require.NoError(t, err)
	require.Len(t, recent, 3)

	// Pagination: limit 2 yields a cursor that walks the rest.
	page1, next, err := st.ListNotes(ctx, store.NoteFilter{GranteeID: "acme", Limit: 2})
	require.NoError(t, err)
	require.Len(t, page1, 2)
	require.NotZero(t, next)
	page2, _, err := st.ListNotes(ctx, store.NoteFilter{GranteeID: "acme", Limit: 2, Cursor: next})
	require.NoError(t, err)
	require.Len(t, page2, 2)
	require.Less(t, page2[0].ID, page1[1].ID)
}
