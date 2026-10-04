import { useEffect, useRef, useState } from "react";
import { api } from "@/api";
import { ErrorAlert } from "@/components/error-alert";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

export function GitHubAppKey({
  project,
  replacing = false,
  onSaved,
  onCancel,
}: {
  project: string;
  replacing?: boolean;
  onSaved: () => void;
  onCancel: () => void;
}) {
  const input = useRef<HTMLInputElement>(null);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const [selected, setSelected] = useState(false);
  useEffect(() => {
    const element = input.current;
    return () => {
      if (element) element.value = "";
    };
  }, []);
  const clear = () => {
    if (input.current) input.current.value = "";
    setSelected(false);
  };
  return (
    <form
      className="space-y-3 rounded-lg border p-4"
      aria-label="Set up GitHub bot"
      onSubmit={async (event) => {
        event.preventDefault();
        const file = input.current?.files?.[0];
        if (!file || pending) return;
        if (file.size > 65536) {
          clear();
          setError("Choose a private key file smaller than 64 KB.");
          return;
        }
        setPending(true);
        setError("");
        try {
          // Read only for this request; never put key bytes in React Query or storage.
          await api.saveGitHubIdentity(project, await file.text());
          clear();
          onSaved();
        } catch (failure) {
          clear();
          setError(
            failure instanceof Error
              ? failure.message
              : "The key could not be verified.",
          );
        } finally {
          setPending(false);
        }
      }}
    >
      <h4 className="text-sm font-medium">
        {replacing
          ? "Update your bot’s signing key"
          : "Finish GitHub bot setup"}
      </h4>
      <p className="text-sm text-muted-foreground">
        {replacing
          ? "Choose a key from your existing GitHub App. Circular verifies it before replacing the saved key."
          : "Your existing GitHub connection stays connected. This one-time step lets Circular publish as your app."}
      </p>
      <ol className="list-decimal space-y-1 pl-5 text-sm text-muted-foreground">
        <li>Open your existing GitHub App’s settings.</li>
        <li>
          Under <strong>Private keys</strong>, select{" "}
          <strong>Generate a private key</strong>.
        </li>
        <li>
          Choose the downloaded <strong>.pem</strong> file below.
        </li>
      </ol>
      <a
        className="text-sm underline underline-offset-4"
        href="https://github.com/settings/apps"
        target="_blank"
        rel="noreferrer"
      >
        Open GitHub App settings
      </a>
      <div className="grid gap-2">
        <Label htmlFor="github-identity-key">GitHub App private key</Label>
        <Input
          ref={input}
          id="github-identity-key"
          type="file"
          accept=".pem"
          disabled={pending}
          onChange={() => {
            setError("");
            setSelected(!!input.current?.files?.length);
          }}
        />
      </div>
      {error && <ErrorAlert>{error}</ErrorAlert>}
      <div className="flex flex-wrap gap-2">
        <Button type="submit" disabled={pending || !selected}>
          {pending ? "Verifying key…" : "Verify and enable bot"}
        </Button>
        <Button
          type="button"
          variant="ghost"
          disabled={pending}
          onClick={() => {
            clear();
            onCancel();
          }}
        >
          Cancel
        </Button>
      </div>
    </form>
  );
}
