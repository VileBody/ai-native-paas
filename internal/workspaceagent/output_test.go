package workspaceagent

import (
	"bytes"
	"testing"

	workspacev1 "github.com/keir-research/ai-native-paas/pkg/contracts/workspace/v1"
)

func TestWorkspaceAgentOutput_RestartCanReplayBoundedRedactedChunks(t *testing.T) {
	sink := FileOutputSink{Directory: t.TempDir()}
	stdout := bytes.Repeat([]byte("a"), (32<<10)+7)
	stderr := []byte("safe-error")
	if err := sink.Persist("command-1", stdout, stderr, true); err != nil {
		t.Fatal(err)
	}
	var chunks []workspacev1.AgentOutputChunk
	if err := sink.Stream("command-1", func(chunk workspacev1.AgentOutputChunk) error {
		chunk.SessionID = "session-1"
		if chunk.Validate() != nil {
			t.Fatalf("invalid chunk=%#v", chunk)
		}
		chunks = append(chunks, chunk)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 3 || chunks[0].Stream != workspacev1.AgentOutputStdout || chunks[0].Final || !chunks[1].Final || !chunks[1].Truncated || chunks[2].Stream != workspacev1.AgentOutputStderr || !chunks[2].Final || !chunks[2].Truncated {
		t.Fatalf("chunks=%#v", chunks)
	}
	if rebuilt := append(append([]byte(nil), chunks[0].Data...), chunks[1].Data...); !bytes.Equal(rebuilt, stdout) || !bytes.Equal(chunks[2].Data, stderr) {
		t.Fatal("streamed output changed after durable replay")
	}
}
