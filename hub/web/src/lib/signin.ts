// What the hub says in an address when a sign-in failed: a code from this list. Anybody can write an address and send it to somebody, so what it
// says is never shown: a code has words here, and anything else has the same few words.

const reasons = new Map<string, string>([
  ["no_sso", "Single sign-on is not configured."],
  ["not_started", "The login did not start here. Try again."],
  ["bad_state", "The login could not be checked. Try again."],
  ["provider_refused", "The login provider refused. Try again, or ask an operator: the hub's log has why."],
  ["login_failed", "The login failed. Try again, or ask an operator: the hub's log has why."],
  ["no_invite", "An account on this hub takes an invite. Ask a project's owner or an operator for one, and open its link."],
  ["bad_invite", "This invite is no longer good: it was used, it expired or it was revoked. Ask for a new one."],
  ["refused", "Sign-in refused."],
  ["link_stale", "The link was started by somebody who is no longer signed in here. Sign in and try again."],
  ["identity_taken", "That account at the provider already belongs to somebody else on this hub. An operator can disable that user and remove the account from it."],
  ["issuer_linked", "You already have an account at this provider linked. Unlink it first."],
  ["link_failed", "Linking failed. Try again, or ask an operator: the hub's log has why."],
]);

/** The words for a code from an address, or null when there is no code. */
export function failureText(code: string | null): string | null {
  if (code === null) return null;
  return reasons.get(code) ?? "That did not work. Try again.";
}

/** A sign-in worked, and the invite it came with did not. */
export const inviteFailedText = "This invite is no longer good: it was used, it expired or it was revoked. Ask for a new one.";
