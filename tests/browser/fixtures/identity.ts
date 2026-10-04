import type { components } from "../../../apps/web/src/generated/api";
export function legacyIdentity(
  provider: "github" | "linear",
): components["schemas"]["IdentityStatus"] {
  return {
    provider,
    mode: "user",
    identity_id: "",
    account_id: "",
    account_name: "",
    actor_id: "",
    actor_name: "",
    actor_login: "",
    avatar_url: "",
    status: "needs_setup",
    capabilities: [],
    affected_projects: [],
    environment_managed: false,
    reason: "",
  };
}
