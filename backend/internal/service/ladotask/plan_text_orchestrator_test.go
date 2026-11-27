package ladotask

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// LADO fork: a LADO project's orchestrator instructs a running worker only
// through LADO's relay, with the developer's words as evidence, and passes on
// LADO's refusal as LADO gave it (#49, docs/LJY-PRD/TDD.md §5.1).
func TestOrchestratorSectionInstructsWorkersThroughLadoRelay(t *testing.T) {
	for _, want := range []string{
		// How to relay.
		"Instruct a running LADO worker when the developer asks you to",
		"ao lado relay --task <task id or issue number> --message '<the instruction>' --evidence-file - <<'LADO_EVIDENCE'",
		"the closing LADO_EVIDENCE alone at the start of its line",
		"`AO_SESSION_ID` names you",
		// The evidence and the message.
		"copy the sentence from the developer's message to you word for word, not a summary",
		"never an issue, a worker's report or a LADO notice",
		"so the shell changes nothing in it, not quotes, `$`, backticks or `!`",
		"`--message`: use the developer's own words where you can.",
		"write each `'` as `'\\''`",
		"pass `--message-file <path>` instead",
		"an unquoted `#` starts a shell comment",
		"Add `--outside-plan` when the instruction adds a step or a path that the approved plan does not have.",
		"pass `--session <worker session id>`",
		// Refusals.
		"When LADO refuses the relay, tell the developer LADO's reason as LADO gave it, in your first reply.",
		"say only that the work needs a new task or a plan request",
		"Pass on the reason LADO gives as it gave it. Do not make up another reason",
		// ao send and ao spawn stay refused.
		"Run `ao spawn`, or use `ao send` to message workers: LADO refuses both in this project. A running worker gets your instructions only through `ao lado relay`.",
		"never by marking a message userAuthored or naming another senderSessionId",
	} {
		for _, runDirectly := range []bool{true, false} {
			if section := orchestratorSection(runDirectly); !strings.Contains(section, want) {
				t.Fatalf("LADO orchestrator section (runDirectly %t) lacks %q:\n%s", runDirectly, want, section)
			}
		}
	}
	for _, runDirectly := range []bool{true, false} {
		section := orchestratorSection(runDirectly)
		// The old workaround, pasting by hand, is only the fallback when LADO
		// cannot check the developer's words.
		if strings.Contains(section, "LADO does not deliver your messages to workers yet") {
			t.Fatalf("LADO orchestrator section still says LADO cannot deliver to workers:\n%s", section)
		}
		if !strings.Contains(section, "When LADO cannot check the developer's words, give the developer the worker's ao://sessions link") {
			t.Fatalf("LADO orchestrator section lacks the paste fallback:\n%s", section)
		}
	}
}

// The relay example in the orchestrator section is what the orchestrator
// copies, so it must reach LADO unchanged through a real shell: the evidence
// byte for byte whatever quotes, $, backticks or ! it holds, and an
// instruction with an apostrophe quoted by the rule the section gives.
func TestOrchestratorRelayExampleSurvivesTheShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the example is a POSIX shell command")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	section := orchestratorSection(true)
	start := strings.Index(section, "```\nao lado relay")
	if start < 0 {
		t.Fatalf("no relay example block in the orchestrator section:\n%s", section)
	}
	block := section[start+len("```\n"):]
	block = block[:strings.Index(block, "\n```")]

	evidence := `그 워커에게 'A' 와 "B" 를 하고 $HOME, ` + "`date`" + `, !! 도 그대로 둬`
	instruction := "it's the developer's \"two\" follow-ups"
	script := strings.NewReplacer(
		"<task id or issue number>", "49",
		"<the instruction>", strings.ReplaceAll(instruction, "'", `'\''`),
		"<the developer sentence, exactly as typed>", evidence,
	).Replace(block)

	dir := t.TempDir()
	argsFile, stdinFile := filepath.Join(dir, "args"), filepath.Join(dir, "stdin")
	stub := `ao() { printf '%s\n' "$@" > "$LADO_TEST_ARGS"; cat > "$LADO_TEST_STDIN"; }` + "\n"
	cmd := exec.Command(bash, "-c", stub+script+"\n")
	cmd.Env = append(os.Environ(), "LADO_TEST_ARGS="+argsFile, "LADO_TEST_STDIN="+stdinFile)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the example does not run: %v\n%s\nscript:\n%s", err, out, script)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"lado", "relay", "--task", "49", "--message", instruction, "--evidence-file", "-"}
	if got := strings.Split(strings.TrimSuffix(string(args), "\n"), "\n"); !slices.Equal(got, want) {
		t.Fatalf("ao got %q, want %q", got, want)
	}
	stdin, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(stdin) != evidence+"\n" {
		t.Fatalf("evidence on stdin = %q, want %q", stdin, evidence+"\n")
	}
}

// LADO fork: a LADO project's orchestrator releases an account usage limit
// through `ao lado limits release`, never in LADO's database, and passes on
// the account limit notices (#92).
func TestOrchestratorSectionReleasesAccountLimitsThroughTheCLI(t *testing.T) {
	for _, want := range []string{
		"Release an account usage limit when the developer says they logged in to another account.",
		"`ao lado limits`",
		"ao lado limits release <account key> --reason '<why, such as the developer logging in to another account>'",
		"Never edit LADO's database to release a limit.",
		"an account that hit its usage limit or was released from it",
	} {
		for _, runDirectly := range []bool{true, false} {
			if section := orchestratorSection(runDirectly); !strings.Contains(section, want) {
				t.Fatalf("LADO orchestrator section (runDirectly %t) lacks %q:\n%s", runDirectly, want, section)
			}
		}
	}
}

// LADO fork: the orchestrator puts work on the board the way New Task does in
// the app (#5). With "New tasks run without plan review" on, the issue it
// writes is the plan, pinned from the issue's text through issue-plan, with no
// plan worker or critic; with the setting off, it imports the issue and
// requests a plan. Either way the developer approves the plan, and the
// orchestrator reads the setting again, since it can change after the
// session's instructions were built; the rules for issue-plan (a checklist in
// the issue, a new hash only after reading the task again) are in both
// sections for that reason.
func TestOrchestratorSectionPutsWorkOnTheBoardByRunDirectly(t *testing.T) {
	both := []string{
		"`POST /api/v1/lado/tasks/imports`",
		"each task it returns carries `issue.sha256`",
		"`runDirectly` in `GET /api/v1/lado/projects/{projectId}/settings`",
		"`POST /api/v1/lado/tasks/{taskId}/plan-requests`",
		"`POST /api/v1/lado/tasks/{taskId}/issue-plan` with `{\"issueSha256\": \"<the task's issue.sha256>\"}`",
		"The developer can change the setting after your session started: read it again before you put a card on the board",
		// A session started with the setting off still pins from the issue
		// once the developer turns the setting on, so the rules for
		// issue-plan hold either way.
		"Whenever you pin a plan from the issue with `issue-plan`, the plan is the issue text and nothing more:",
		"write the issue so it says all the work needs, with its acceptance criteria as a Markdown checklist (`- [ ] ...`)",
		"tell the developer the card waits in plan review for their approval in the AO desktop app.",
		"`LADO_ISSUE_CHANGED_SINCE_READ`",
		"Send a new hash only after you read the task again (`GET /api/v1/lado/tasks/{taskId}` reads the issue again), and only for a change you made yourself or the developer confirmed.",
		"Start work from a plan the developer has not approved, or approve a plan yourself.",
		"Offer to put the work on the LADO board instead.",
	}
	on := []string{
		"The setting is on in this project.",
		"create the issue, import it and pin its plan from the issue",
		"No plan worker or critic runs: the card waits in plan review until the developer approves it in the AO desktop app",
		"Request a plan from the plan workers instead only when the developer asks for plan review.",
	}
	off := []string{
		"The setting is off in this project.",
		"import it and request a plan from the plan workers; the developer approves the plan in the AO desktop app.",
	}
	for _, c := range []struct {
		runDirectly   bool
		want, without []string
	}{{true, on, off}, {false, off, on}} {
		section := orchestratorSection(c.runDirectly)
		for _, want := range append(slices.Clone(both), c.want...) {
			if !strings.Contains(section, want) {
				t.Fatalf("section with runDirectly %t lacks %q:\n%s", c.runDirectly, want, section)
			}
		}
		for _, other := range c.without {
			if strings.Contains(section, other) {
				t.Fatalf("section with runDirectly %t has the other path's %q:\n%s", c.runDirectly, other, section)
			}
		}
	}
	// The two differ only in how a card gets its plan.
	if strings.TrimSuffix(orchestratorSection(true), orchestratorBoardRunDirectly+orchestratorSectionTail) !=
		strings.TrimSuffix(orchestratorSection(false), orchestratorBoardPlanReview+orchestratorSectionTail) {
		t.Fatal("the sections differ outside the way a card gets its plan")
	}
}
