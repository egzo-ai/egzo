<script lang="ts">
  import { stamp, until } from "../lib/format";
  import type { Invite } from "../lib/types";

  let { invites, onrevoke, wide = false }: { invites: Invite[]; onrevoke: (i: Invite) => void; wide?: boolean } = $props();

  const grants = (i: Invite) => {
    if (i.project) return wide ? `${i.role} on ${i.projectName ?? i.project}` : (i.role ?? "");
    const parts = [i.operator ? "operator" : "", i.projectQuota ? `quota of ${i.projectQuota}` : ""].filter(Boolean);
    return parts.length ? parts.join(", ") : "an account";
  };
</script>

<div class="table-wrap">
  <table class="table" data-testid="invites">
    <thead><tr><th>Invite</th><th>Grants</th><th>Made by</th><th>Expires</th><th></th></tr></thead>
    <tbody>
      {#each invites as i (i.id)}
        <tr data-invite={i.id}>
          <td class="muted">{i.id}</td>
          <td>{grants(i)}</td>
          <td>{i.createdBy}</td>
          <td class="muted" title={stamp(i.expiresAt)}>{until(i.expiresAt)}</td>
          <td class="actions"><button class="btn" data-size="sm" aria-label="Revoke invite {i.id}" onclick={() => onrevoke(i)}>Revoke</button></td>
        </tr>
      {/each}
    </tbody>
  </table>
</div>
