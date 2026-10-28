import { useMutation, useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { api } from "@/api";
import { Button } from "@/components/ui/button";
import { ErrorAlert } from "@/components/error-alert";

export function TaskApproval({ taskID }: { taskID: string }) {
  const navigate = useNavigate();
  const approval = useQuery({
    queryKey: ["task-approval", taskID],
    queryFn: () => api.taskApproval(taskID),
    retry: false,
  });
  const propose = useMutation({
    mutationFn: () => {
      if (!approval.data) throw new Error("Load the current scope first.");
      const input = approval.data.proposal;
      return api.proposeTask({
        ...input,
        request_key: `confirm:${taskID}:${input.expected_task_revision}`,
        proposer: "Circular console",
      });
    },
    onSuccess: (p) =>
      void navigate({
        to: "/proposals/$proposalId",
        params: { proposalId: p.id },
        search: { revision: p.revision },
      }),
  });
  return (
    <section className="space-y-3 rounded-md border p-4">
      <h3 className="font-medium">Scope approval</h3>
      {approval.data?.approved ? (
        <p>The current scope has a recorded user approval.</p>
      ) : approval.data ? (
        <>
          <p>
            Approval of this scope is unknown. Existing history stays intact.
            New work needs a user decision.
          </p>
          <Button
            variant="outline"
            disabled={propose.isPending}
            onClick={() => propose.mutate()}
          >
            Review current scope
          </Button>
        </>
      ) : null}
      {(approval.error || propose.error) && (
        <ErrorAlert>{(approval.error || propose.error)?.message}</ErrorAlert>
      )}
    </section>
  );
}
