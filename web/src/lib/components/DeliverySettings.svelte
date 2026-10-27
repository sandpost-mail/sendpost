<script lang="ts">
  import { onMount } from 'svelte';
  import { api, errorMessage } from '$lib/api';
  import {
    confirmationFor,
    connectionChanged,
    deliveryWarnings,
    formatAllowlist,
    hostError,
    parseAllowlist,
    portError,
    sesPreset
  } from '$lib/delivery';
  import type { DeliveryMode, DeliveryPolicy, TlsMode, Transport, TransportCheck, TransportKind } from '$lib/types';
  import TextField from './TextField.svelte';

  let { projectId }: { projectId: number } = $props();

  let loaded = $state(false);
  let loadError = $state('');
  let transport = $state<Transport | null>(null);
  let policy = $state<DeliveryPolicy | null>(null);

  // Transport form.
  let kind = $state<TransportKind>('smtp');
  let host = $state('');
  let port = $state('587');
  let tlsMode = $state<TlsMode>('starttls');
  let username = $state('');
  let password = $state('');
  let sender = $state('');
  let transportError = $state('');
  let transportNotice = $state('');
  let hostErr = $state('');
  let portErr = $state('');
  let check = $state<TransportCheck | null>(null);
  let busyTransport = $state(false);

  // Policy form.
  let mode = $state<DeliveryMode>('capture');
  let allowText = $state('');
  let redirect = $state('');
  let prefix = $state('');
  let policyError = $state('');
  let policyNotice = $state('');
  let busyPolicy = $state(false);

  onMount(async () => {
    try {
      [transport, policy] = await Promise.all([api.getTransport(projectId), api.getPolicy(projectId)]);
      fillTransport(transport);
      fillPolicy(policy);
    } catch (e) {
      loadError = errorMessage(e);
    } finally {
      loaded = true;
    }
  });

  function fillTransport(t: Transport | null) {
    if (!t) return;
    kind = t.kind;
    host = t.host;
    port = String(t.port);
    tlsMode = t.tls_mode;
    username = t.username;
    sender = t.sender;
    password = '';
  }

  function fillPolicy(p: DeliveryPolicy) {
    mode = p.mode;
    allowText = formatAllowlist(p.allowlist);
    redirect = p.redirect_address;
    prefix = p.subject_prefix;
  }

  function useSes() {
    kind = 'ses';
    const preset = sesPreset('us-east-1');
    host = host || preset.host;
    port = String(preset.port);
    tlsMode = preset.tls_mode;
  }

  let formTransport = $derived<Transport | null>(transport);
  let needsPassword = $derived(
    username.trim() !== '' &&
      (transport === null ||
        connectionChanged(transport, { kind, host, port: Number(port), username, tls_mode: tlsMode }) ||
        !transport.has_secret)
  );
  let warnings = $derived(
    deliveryWarnings({ mode, allowlist: parseAllowlist(allowText), redirect_address: redirect }, formTransport)
  );

  async function saveTransport(event: SubmitEvent, enabled = transport?.enabled ?? false) {
    event.preventDefault();
    transportError = transportNotice = '';
    hostErr = hostError(host);
    portErr = portError(port);
    if (hostErr || portErr) return;
    if (needsPassword && password === '') {
      transportError = 'Enter the password again: the connection settings changed or no password is stored.';
      return;
    }
    busyTransport = true;
    try {
      transport = await api.putTransport(projectId, {
        kind,
        host: host.trim(),
        port: Number(port),
        username: username.trim(),
        ...(password !== '' || username.trim() === '' ? { secret: password } : {}),
        tls_mode: tlsMode,
        sender: sender.trim(),
        enabled
      });
      fillTransport(transport);
      check = null;
      transportNotice = transport.enabled
        ? 'Saved. The transport is enabled.'
        : 'Saved. The transport is disabled: test the connection, then enable it.';
    } catch (e) {
      transportError = errorMessage(e);
    } finally {
      busyTransport = false;
    }
  }

  async function setEnabled(enabled: boolean) {
    if (!transport) return;
    if (enabled && !confirm('Enable this transport? Mail that the policy allows will be sent for real.')) return;
    transportError = transportNotice = '';
    busyTransport = true;
    try {
      transport = await api.putTransport(projectId, {
        kind: transport.kind,
        host: transport.host,
        port: transport.port,
        username: transport.username,
        tls_mode: transport.tls_mode,
        sender: transport.sender,
        enabled
      });
      transportNotice = enabled ? 'The transport is enabled.' : 'The transport is disabled.';
    } catch (e) {
      transportError = errorMessage(e);
    } finally {
      busyTransport = false;
    }
  }

  async function test() {
    transportError = transportNotice = '';
    busyTransport = true;
    try {
      check = await api.testTransport(projectId);
    } catch (e) {
      transportError = errorMessage(e);
    } finally {
      busyTransport = false;
    }
  }

  async function removeTransport() {
    if (!confirm('Remove the transport and its stored password? Nothing can be sent until a new one is set.')) return;
    transportError = transportNotice = '';
    try {
      await api.deleteTransport(projectId);
      transport = null;
      check = null;
      host = username = password = sender = '';
      kind = 'smtp';
      port = '587';
      tlsMode = 'starttls';
    } catch (e) {
      transportError = errorMessage(e);
    }
  }

  async function savePolicy(event: SubmitEvent) {
    event.preventDefault();
    policyError = policyNotice = '';
    const allowlist = parseAllowlist(allowText);
    const text = confirmationFor(mode, allowlist, redirect);
    if (text && !confirm(text)) return;
    busyPolicy = true;
    try {
      policy = await api.putPolicy(projectId, {
        mode,
        allowlist,
        redirect_address: redirect.trim(),
        subject_prefix: prefix
      });
      fillPolicy(policy);
      policyNotice = 'Policy saved.';
    } catch (e) {
      policyError = errorMessage(e);
    } finally {
      busyPolicy = false;
    }
  }
</script>

<section class="card stack" aria-labelledby="delivery-heading">
  <h2 id="delivery-heading">Delivery</h2>
  <p class="muted">
    Optional and off by default. Captured mail always stays in Sandpost. With a transport and a policy, some of it can
    also be sent out for real. If anything is missing or unclear, nothing is sent.
  </p>

  {#if loadError}<p class="error" role="alert">{loadError}</p>{/if}
  {#if !loaded}
    <p class="muted">Loading…</p>
  {:else if !loadError}
    <form class="stack" onsubmit={(e) => saveTransport(e)} novalidate aria-labelledby="transport-heading">
      <h3 id="transport-heading">Transport (outbound SMTP relay)</h3>
      {#if transport}
        <p>
          {#if transport.enabled}<span class="badge danger">enabled</span>{:else}<span class="badge">disabled</span>{/if}
          <span class="muted">
            {transport.host}:{transport.port}, {transport.tls_mode}
            {#if transport.has_secret}, password stored{/if}
          </span>
        </p>
      {/if}
      <div class="row">
        <label for="tr-kind">Type</label>
        <select id="tr-kind" class="input" bind:value={kind} onchange={() => kind === 'ses' && useSes()}>
          <option value="smtp">Generic SMTP</option>
          <option value="ses">Amazon SES (SMTP)</option>
        </select>
      </div>
      {#if kind === 'ses'}
        <p class="muted">
          Use the SES SMTP endpoint of your region and the SMTP credentials created in the SES console (not your AWS
          keys). The sender must be a verified identity.
        </p>
      {/if}
      <TextField id="tr-host" label="Host" bind:value={host} error={hostErr} placeholder="smtp.example.com" />
      <div class="row">
        <TextField id="tr-port" label="Port" bind:value={port} error={portErr} inputmode="numeric" />
        <div class="field">
          <label for="tr-tls">Encryption</label>
          <select id="tr-tls" class="input" bind:value={tlsMode}>
            <option value="starttls">STARTTLS (required, port 587)</option>
            <option value="implicit">TLS from the start (port 465)</option>
            <option value="none">None (plaintext, no password allowed)</option>
          </select>
        </div>
      </div>
      {#if tlsMode === 'none'}
        <p class="warn" role="note">
          Plaintext SMTP: the mail travels unencrypted and a username and password are not allowed. Only use this for a
          relay on a trusted network.
        </p>
      {/if}
      <TextField id="tr-user" label="Username" bind:value={username} autocomplete="off" />
      <TextField
        id="tr-pass"
        label="Password"
        type="password"
        bind:value={password}
        autocomplete="new-password"
        placeholder={transport?.has_secret ? '(stored, leave empty to keep it)' : ''}
        hint={needsPassword
          ? 'Required: the connection settings changed or no password is stored. It is encrypted at rest and never shown again.'
          : 'Encrypted at rest, never shown again.'}
      />
      <TextField
        id="tr-sender"
        label="Sender address (envelope)"
        bind:value={sender}
        placeholder="noreply@example.com"
        hint="Bounces go here. Empty uses the null sender. Required for SES."
      />
      <div class="row">
        <button class="btn primary" disabled={busyTransport}>Save transport</button>
        {#if transport}
          <button type="button" class="btn" disabled={busyTransport} onclick={test}>Test connection</button>
          {#if transport.enabled}
            <button type="button" class="btn" disabled={busyTransport} onclick={() => setEnabled(false)}>Disable</button>
          {:else}
            <button type="button" class="btn danger" disabled={busyTransport} onclick={() => setEnabled(true)}>Enable…</button>
          {/if}
          <button type="button" class="btn danger" onclick={removeTransport}>Remove</button>
        {/if}
      </div>
      <p class="muted">
        A new transport starts disabled. Changing the host, port, username, encryption or type disables it again.
        Certificates are always verified. The connection test only opens a connection and logs in, it sends no mail.
      </p>
      {#if transportError}<p class="error" role="alert">{transportError}</p>{/if}
      {#if transportNotice}<p role="status">{transportNotice}</p>{/if}
      {#if check}
        <p role="status" class={check.ok ? '' : 'error'}>
          {check.ok ? `Connection works${check.tls ? ' (encrypted' : ' (not encrypted'}${check.authenticated ? ', logged in)' : ')'}.` : `Connection test failed: ${check.error ?? 'unknown error'}`}
        </p>
      {/if}
    </form>

    <hr />

    <form class="stack" onsubmit={savePolicy} novalidate aria-labelledby="policy-heading">
      <h3 id="policy-heading">Policy</h3>
      <fieldset class="modes">
        <legend>What happens to captured mail</legend>
        <label class="radio">
          <input type="radio" name="mode" value="capture" bind:group={mode} />
          <span><strong>Capture only</strong> (default). Nothing is ever sent out.</span>
        </label>
        <label class="radio">
          <input type="radio" name="mode" value="allow" bind:group={mode} />
          <span><strong>Allow</strong>. Recipients on the allowlist get the mail for real, the others get nothing.</span>
        </label>
        <label class="radio">
          <input type="radio" name="mode" value="redirect" bind:group={mode} />
          <span>
            <strong>Redirect</strong>. Allowlisted recipients get the mail for real, every other recipient is replaced by
            one address (the original recipients are kept in <code>X-Original-To</code>).
          </span>
        </label>
      </fieldset>

      <div class="field">
        <label for="po-allow">Allowlist</label>
        <textarea id="po-allow" class="input" rows="4" bind:value={allowText} placeholder={'dev@example.com\n@example.org'}></textarea>
        <span class="muted hint">
          One entry per line: an address (<code>dev@example.com</code>) or a whole domain (<code>@example.com</code>).
          No wildcards, subdomains are not included.
        </span>
      </div>
      {#if mode === 'redirect'}
        <TextField id="po-redirect" label="Redirect address" type="email" bind:value={redirect} placeholder="qa@example.com" />
        <TextField id="po-prefix" label="Subject prefix (redirected copies)" bind:value={prefix} maxlength={100} placeholder="[staging]" />
      {/if}

      {#each warnings as w, i (i)}
        <p class={w.level === 'danger' ? 'warn danger' : 'warn'} role={w.level === 'danger' ? 'alert' : 'note'}>{w.text}</p>
      {/each}

      <div><button class="btn primary" disabled={busyPolicy}>Save policy</button></div>
      {#if policyError}<p class="error" role="alert">{policyError}</p>{/if}
      {#if policyNotice}<p role="status">{policyNotice}</p>{/if}
      <p class="muted">
        Manual release of a single message uses the allowlist too, in any mode, and needs an enabled transport.
      </p>
    </form>
  {/if}
</section>

<style>
  .warn { border: 1px solid var(--border); background: var(--surface-2); border-radius: var(--radius); padding: 8px 12px; margin: 0; }
  .warn.danger { border-color: var(--danger); background: var(--danger-soft); color: var(--text); font-weight: 600; }
  fieldset { border: 1px solid var(--border); border-radius: var(--radius); padding: 8px 12px; display: flex; flex-direction: column; gap: 8px; }
  .radio { display: flex; gap: 8px; align-items: flex-start; }
  textarea.input { font-family: var(--mono); width: 100%; }
  .field { display: flex; flex-direction: column; gap: 4px; }
  hr { border: 0; border-top: 1px solid var(--border); width: 100%; }
  .hint { font-size: 0.85em; }
</style>
