import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Copy, ExternalLink, Radio } from "lucide-react";
import { api, type Provider } from "@/api";
import { ErrorAlert } from "@/components/error-alert";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

export function IntegrationWebhooks({
  provider,
  title = "Incoming events",
}: {
  provider: Provider;
  title?: string;
}) {
  const name = provider === "github" ? "GitHub" : "Linear";
  const queryKey = ["integration-webhooks", provider];
  const client = useQueryClient();
  const settings = useQuery({
    queryKey,
    queryFn: () => api.webhookSettings(provider),
    retry: false,
  });
  const [editing, setEditing] = useState(false);
  const [origin, setOrigin] = useState("");
  const secret = useRef<HTMLInputElement>(null);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    if (settings.data) setOrigin(settings.data.public_origin);
  }, [settings.data]);
  const check = useMutation({
    mutationFn: () => api.checkWebhookSettings(provider),
    onSuccess: (data) => client.setQueryData(queryKey, data),
    onError: () => client.invalidateQueries({ queryKey }),
  });
  const state = settings.data;
  const status = state?.status;
  const receptionStatus =
    status === "receiving"
      ? "Receiving events"
      : status === "waiting"
        ? "Waiting for a verified delivery"
        : status === "needs_access" || status === "configuration_failed"
          ? "Reception needs attention"
          : "Reception is not configured";
  return (
    <section
      aria-label={`${name} incoming events`}
      className="space-y-3 border-t pt-4"
    >
      <h4 className="flex items-center gap-2 text-sm font-semibold">
        <Radio className="size-4" aria-hidden="true" />
        {title}
      </h4>
      <p role="status" className="text-sm">
        {receptionStatus}
      </p>
      {state?.last_verified_at && (
        <p className="text-xs text-muted-foreground">
          Last verified delivery:{" "}
          {new Date(state.last_verified_at).toLocaleString()}
        </p>
      )}
      {state?.reason && (
        <p className="text-sm text-muted-foreground">{state.reason}</p>
      )}
      {state?.callback_url && (
        <div className="flex items-start gap-2">
          <code className="min-w-0 flex-1 break-all text-xs">
            {state.callback_url}
          </code>
          <Button
            variant="ghost"
            size="icon"
            aria-label="Copy webhook callback address"
            onClick={async () => {
              try {
                await navigator.clipboard.writeText(state.callback_url);
                setCopied(true);
              } catch {
                setError("Select and copy the callback address above.");
              }
            }}
          >
            <Copy aria-hidden="true" />
          </Button>
        </div>
      )}
      {copied && (
        <p role="status" className="text-xs">
          Callback address copied.
        </p>
      )}
      {editing ? (
        <form
          className="space-y-3"
          onSubmit={async (e) => {
            e.preventDefault();
            setSaving(true);
            setError("");
            try {
              const data = await api.saveWebhookSettings(provider, {
                public_origin: origin,
                signing_secret: secret.current?.value || "",
              });
              client.setQueryData(queryKey, data);
              setEditing(false);
            } catch (problem) {
              setError(
                problem instanceof Error
                  ? problem.message
                  : "Could not save receiver settings",
              );
            } finally {
              if (secret.current) secret.current.value = "";
              setSaving(false);
            }
          }}
        >
          <p className="text-xs text-muted-foreground">
            Applies to this {name} app
            {state?.affected_projects.length
              ? ` and its projects: ${state.affected_projects.join(", ")}`
              : ""}
            .
          </p>
          <div className="space-y-1">
            <Label htmlFor={`${provider}-receiver`}>
              Public receiver address
            </Label>
            <Input
              id={`${provider}-receiver`}
              type="url"
              required
              placeholder="https://receiver.example"
              value={origin}
              onChange={(e) => setOrigin(e.target.value)}
            />
          </div>
          <p className="text-xs text-muted-foreground">
            Use the public HTTPS receiver address supplied by the person running
            Circular. The connection guide explains how to set it up.
          </p>
          {provider === "linear" ? (
            <>
              <div className="space-y-1">
                <Label htmlFor={`${provider}-signing-secret`}>
                  Signing secret
                </Label>
                <Input
                  ref={secret}
                  id={`${provider}-signing-secret`}
                  type="password"
                  autoComplete="new-password"
                  required={!state?.secret_configured}
                  placeholder={
                    state?.secret_configured
                      ? "Leave empty to keep the current secret"
                      : "From Linear app webhook settings"
                  }
                />
              </div>
              <p className="text-xs text-muted-foreground">
                Set the callback to{" "}
                {origin.replace(/\/$/, "") || "your receiver address"}
                /webhooks/linear in Linear, then paste its signing secret here.
              </p>
            </>
          ) : (
            <p className="text-xs text-muted-foreground">
              Saving updates the GitHub App's webhook address and configures its
              signing secret.
            </p>
          )}
          <div className="flex flex-wrap gap-2">
            <Button size="sm" type="submit" disabled={saving}>
              Save receiver settings
            </Button>
            <Button
              size="sm"
              variant="outline"
              type="button"
              disabled={saving}
              onClick={() => {
                if (secret.current) secret.current.value = "";
                setEditing(false);
                setError("");
              }}
            >
              Cancel
            </Button>
          </div>
        </form>
      ) : (
        <div className="flex flex-wrap gap-2">
          <Button
            variant="outline"
            size="sm"
            onClick={() => {
              setError("");
              setEditing(true);
            }}
          >
            {state?.secret_configured
              ? "Edit receiver settings"
              : "Configure receiver"}
          </Button>
          {state?.secret_configured && (
            <Button
              variant="outline"
              size="sm"
              disabled={check.isPending}
              onClick={() => check.mutate()}
            >
              Check reception
            </Button>
          )}
        </div>
      )}
      {state?.provider_url && (
        <a
          href={state.provider_url}
          target="_blank"
          rel="noreferrer"
          className="inline-flex items-center gap-1 text-xs text-primary"
        >
          Provider webhook settings{" "}
          <ExternalLink className="size-3" aria-hidden="true" />
        </a>
      )}
      {(error || settings.error || check.error) && (
        <ErrorAlert>
          {error || settings.error?.message || check.error?.message}
        </ErrorAlert>
      )}
    </section>
  );
}
