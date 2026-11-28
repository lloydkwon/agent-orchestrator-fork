package ladotask

import (
	"context"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladoknowledge"
)

// orchestratorAnswersHeading starts the part of the LADO orchestrator section
// that says how it answers the developer.
const orchestratorAnswersHeading = "How you answer the developer."

// LADO fork: a LADO project's orchestrator answers the developer result
// first, then what the developer has to do, then the terms they may not know,
// and leaves internal evidence out unless the developer asks for it (#15).
// A turn a LADO notice starts gets two to four lines of what the notice means
// for the developer, and the knowledge notes it writes stay out of its
// answers.
func TestOrchestratorSectionSetsHowItAnswersTheDeveloper(t *testing.T) {
	ordered := []string{
		orchestratorAnswersHeading,
		"Start with the result in one line: what was done and what was not.",
		"Then what the developer has to do",
		"When there is nothing, say there is nothing for them to do.",
		"Explain briefly a term or an abbreviation the developer may not know, the first time it appears.",
	}
	rest := []string{
		"Leave internal evidence out unless the developer asks for it: commit hashes, session and request ids, the checks and conditions behind a verdict, and the knowledge notes you wrote or updated.",
		// The rules above that pass something on as it is still hold.
		"LADO's reason as LADO gave it",
		"an ao://sessions link the developer needs",
		"Write times in the developer's time zone",
		"convert a time that LADO, a log or an API gives in UTC",
		"When a turn starts with a LADO notice (a message that starts with \"" + domain.LadoNoticePrefix + "\")",
		"tell the developer in two to four lines only what it means for them",
		"Do not retell what you checked to find that out.",
		"Keep writing knowledge notes as the Project knowledge (LADO) section says, but do not mention in your answer that you wrote or updated one.",
	}
	for _, runDirectly := range []bool{true, false} {
		section := orchestratorSection(runDirectly)
		_, answers, ok := strings.Cut(section, orchestratorAnswersHeading)
		if !ok {
			t.Fatalf("LADO orchestrator section (runDirectly %t) lacks %q:\n%s", runDirectly, orchestratorAnswersHeading, section)
		}
		answers = orchestratorAnswersHeading + answers
		at := 0
		for _, want := range ordered {
			i := strings.Index(answers[at:], want)
			if i < 0 {
				t.Fatalf("answers (runDirectly %t) lack %q after the part before it:\n%s", runDirectly, want, answers)
			}
			at += i + len(want)
		}
		for _, want := range rest {
			if !strings.Contains(answers, want) {
				t.Fatalf("answers (runDirectly %t) lack %q:\n%s", runDirectly, want, answers)
			}
		}
		// The part closes the section, after the rules it must not override.
		if relay := strings.Index(section, "When LADO refuses the relay"); relay < 0 || relay > strings.Index(section, orchestratorAnswersHeading) {
			t.Fatalf("answers (runDirectly %t) come before the relay rules:\n%s", runDirectly, section)
		}
	}
}

// Only a LADO project's orchestrator gets the part: an orchestrator outside
// LADO mode keeps AO's instructions, and a worker and the plan workers keep
// theirs (#15).
func TestOnlyTheLadoOrchestratorGetsHowToAnswer(t *testing.T) {
	pf := newPlanFixture(t)
	ctx := context.Background()
	orchestrator, worker, designer, critic := knowledgeSessions(t, pf)
	knowledge := &fakeKnowledge{index: ladoknowledge.Index{ProjectID: "demo", Dir: "/state/lado/knowledge/demo"}}
	source := PromptSource{Settings: pf.store, Sessions: pf.store, Policies: pf.store, Knowledge: knowledge}
	standing := func(kind domain.SessionKind, project domain.ProjectID, id domain.SessionID) string {
		t.Helper()
		got, err := source.Standing(ctx, kind, project, id, domain.HarnessClaudeCode)
		if err != nil {
			t.Fatalf("Standing(%s, %s, %s): %v", kind, project, id, err)
		}
		return got.Text
	}

	lado := standing(domain.KindOrchestrator, "demo", orchestrator)
	if !strings.Contains(lado, orchestratorAnswersHeading) {
		t.Fatalf("LADO orchestrator's instructions lack %q:\n%s", orchestratorAnswersHeading, lado)
	}
	// The part closes LADO's section; the knowledge section the part points
	// to follows it.
	if at, know := strings.Index(lado, orchestratorAnswersHeading), strings.Index(lado, knowledgeHeading); know < at {
		t.Fatalf("the knowledge section does not follow the answers:\n%s", lado)
	}

	if got := standing(domain.KindOrchestrator, "other", "other-orchestrator-1"); got != "" {
		t.Fatalf("orchestrator outside LADO mode gets %q, want AO's instructions unchanged", got)
	}
	for _, c := range []struct {
		name string
		id   domain.SessionID
		role string
	}{
		{"worker", worker, knowledgeHeading},
		{"designer", designer, "## LADO plan worker"},
		{"critic", critic, "## LADO plan critic"},
	} {
		got := standing(domain.KindWorker, "demo", c.id)
		if !strings.Contains(got, c.role) {
			t.Fatalf("%s's instructions lack %q:\n%s", c.name, c.role, got)
		}
		if strings.Contains(got, orchestratorAnswersHeading) || strings.Contains(got, "two to four lines") {
			t.Fatalf("%s's instructions hold the orchestrator's answers:\n%s", c.name, got)
		}
	}
}
