package store

import (
	"testing"
)

func newSessionFixture(t *testing.T) (*Store, string) {
	t.Helper()
	s := newTestStore(t)
	sess, err := s.CreateSession("p.md", "test", "m1", "", 1, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s, sess.ID
}

func TestTranscriptOrderAndSiblings(t *testing.T) {
	s, sid := newSessionFixture(t)

	u1, err := s.InsertUserMessage(sid, nil, "hello")
	if err != nil {
		t.Fatal(err)
	}
	rev1 := newTestRev(t, s)
	a1, err := s.InsertAssistantRow(sid, &u1.ID, false, AssistantParams{Model: "m1", PromptRevID: rev1})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishMessage(a1.ID, "resp1", "think1", nil, nil, "done", ""); err != nil {
		t.Fatal(err)
	}
	u2, err := s.InsertUserMessage(sid, &a1.ID, "again")
	if err != nil {
		t.Fatal(err)
	}
	// Another assistant sibling at the same position.
	a1b, err := s.InsertAssistantRow(sid, &u1.ID, false, AssistantParams{Model: "m1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishMessage(a1b.ID, "resp1b", "", nil, nil, "done", ""); err != nil {
		t.Fatal(err)
	}

	sess, err := s.SetHead(sid, u2.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := s.Transcript(sid, sess.HeadMessage)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr) != 3 {
		t.Fatalf("transcript len = %d, want 3 (%+v)", len(tr), ids(tr))
	}
	if tr[0].Role != "user" || tr[0].Text != "hello" {
		t.Errorf("tr[0] = %+v", tr[0])
	}
	if tr[1].ID != a1.ID {
		t.Errorf("tr[1] should be the newest-chain assistant %s, got %s", a1.ID, tr[1].ID)
	}
	if tr[2].ID != u2.ID {
		t.Errorf("tr[2] = %s want %s", tr[2].ID, u2.ID)
	}

	if err := s.FillSiblings(tr); err != nil {
		t.Fatal(err)
	}
	if tr[1].SiblingCount != 2 {
		t.Errorf("assistant siblingCount = %d, want 2", tr[1].SiblingCount)
	}
	if tr[1].SiblingIndex != 1 {
		t.Errorf("assistant siblingIndex = %d, want 1", tr[1].SiblingIndex)
	}
}

func ids(ms []Message) []string {
	out := []string{}
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}

func TestSetHeadToLeaf(t *testing.T) {
	s, sid := newSessionFixture(t)
	u1, _ := s.InsertUserMessage(sid, nil, "a")
	a1, _ := s.InsertAssistantRow(sid, &u1.ID, false, AssistantParams{})
	a2, _ := s.InsertAssistantRow(sid, &u1.ID, false, AssistantParams{}) // newer sibling wins
	a3, _ := s.InsertAssistantRow(sid, &a2.ID, false, AssistantParams{}) // deeper

	sess, err := s.SetHead(sid, u1.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	// Leaf walk from u1: newest child a2 → its child a3.
	if sess.HeadMessage == nil || *sess.HeadMessage != a3.ID {
		t.Fatalf("head = %v, want %s", sess.HeadMessage, a3.ID)
	}
	tr, _ := s.Transcript(sid, sess.HeadMessage)
	wantOrder := []string{u1.ID, a2.ID, a3.ID}
	got := ids(tr)
	for i := range wantOrder {
		if got[i] != wantOrder[i] {
			t.Errorf("transcript[%d] = %s want %s", i, got[i], wantOrder[i])
		}
	}
	_ = a1
}

func TestPreviewLifecycle(t *testing.T) {
	s, sid := newSessionFixture(t)
	u1, _ := s.InsertUserMessage(sid, nil, "q")
	a1, _ := s.InsertAssistantRow(sid, &u1.ID, false, AssistantParams{})
	s.FinishMessage(a1.ID, "old", "", nil, nil, "done", "")

	// Preview of a1: parent = u1.
	conflict, err := s.PreviewConflict(sid, &u1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if conflict {
		t.Fatal("no preview yet")
	}
	pv, err := s.InsertAssistantRow(sid, &u1.ID, true, AssistantParams{})
	if err != nil {
		t.Fatal(err)
	}
	if !pv.Preview {
		t.Error("preview flag not set")
	}
	conflict, _ = s.PreviewConflict(sid, &u1.ID)
	if !conflict {
		t.Error("streaming preview should conflict")
	}

	// Accepting a streaming preview fails.
	if _, err := s.AcceptPreview(pv.ID); err == nil {
		t.Error("accept on streaming preview should error")
	}

	// Finish, then accept.
	s.FinishMessage(pv.ID, "new", "rn", nil, nil, "done", "")
	sess, err := s.AcceptPreview(pv.ID)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if sess.HeadMessage == nil || *sess.HeadMessage != pv.ID {
		t.Fatalf("head after accept = %v want %v", sess.HeadMessage, pv.ID)
	}
	// Now the accepted preview is part of transcripts and conflicts are gone.
	conflict, _ = s.PreviewConflict(sid, &u1.ID)
	if conflict {
		t.Error("accepted row should not conflict as preview")
	}

	// Inserting a second preview deletes old previews for that parent.
	pv2, _ := s.InsertAssistantRow(sid, &u1.ID, true, AssistantParams{})
	_ = pv2
	if err := s.DeletePreviewsForParent(sid, &u1.ID); err != nil {
		t.Fatal(err)
	}
	_, err = s.GetMessage(pv2.ID)
	if err != ErrNotFound {
		t.Error("preview should be deleted")
	}
}

func TestCascadeDeleteClearsHead(t *testing.T) {
	s, sid := newSessionFixture(t)
	u1, _ := s.InsertUserMessage(sid, nil, "a")
	a1, _ := s.InsertAssistantRow(sid, &u1.ID, false, AssistantParams{})
	s.FinishMessage(a1.ID, "x", "", nil, nil, "done", "")
	sess, _ := s.SetHead(sid, a1.ID, false)
	if sess.HeadMessage == nil || *sess.HeadMessage != a1.ID {
		t.Fatal("setup head")
	}
	if err := s.DeleteMessage(a1.ID); err != nil {
		t.Fatal(err)
	}
	sess, _ = s.GetSession(sid)
	if sess.HeadMessage != nil && *sess.HeadMessage == a1.ID {
		t.Error("head should be cleared when its message is deleted")
	}
	// Subtree gone: a1 was leaf; check u1 remains.
	if _, err := s.GetMessage(u1.ID); err != nil {
		t.Errorf("u1 should remain: %v", err)
	}
}

func TestCascadeSubtree(t *testing.T) {
	s, sid := newSessionFixture(t)
	u1, _ := s.InsertUserMessage(sid, nil, "a")
	a1, _ := s.InsertAssistantRow(sid, &u1.ID, false, AssistantParams{})
	u2, _ := s.InsertUserMessage(sid, &a1.ID, "b")
	if err := s.DeleteMessage(u1.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{u1.ID, a1.ID, u2.ID} {
		if _, err := s.GetMessage(id); err != ErrNotFound {
			t.Errorf("descendant %s should be cascade-deleted, err=%v", id, err)
		}
	}
}

func TestPrimaryPath(t *testing.T) {
	s, sid := newSessionFixture(t)
	u1, _ := s.InsertUserMessage(sid, nil, "a")
	a1, _ := s.InsertAssistantRow(sid, &u1.ID, false, AssistantParams{})
	u2, _ := s.InsertUserMessage(sid, &a1.ID, "b")
	a2, _ := s.InsertAssistantRow(sid, &u2.ID, false, AssistantParams{})

	path, err := s.PrimaryPath(sid, u1.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := ids(path)
	want := []string{a1.ID, u2.ID, a2.ID}
	if len(got) != len(want) {
		t.Fatalf("path = %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("path[%d] = %s want %s", i, got[i], want[i])
		}
	}
}
