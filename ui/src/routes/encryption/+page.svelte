<script lang="ts">
	import { onMount } from 'svelte';
	import {
		getEncryption,
		enableEncryption,
		unlockEncryption,
		lockEncryption,
		disableEncryption,
		type Encryption
	} from '$lib/api';

	let enc = $state<Encryption | null>(null);
	let pass = $state('');
	let confirm = $state('');
	let acknowledged = $state(false);
	let remember = $state(false);
	let error = $state('');
	let busy = $state(false);
	let confirmDisable = $state(false);

	const msg = (e: unknown) => (e instanceof Error ? e.message : String(e));

	async function load() {
		try {
			enc = await getEncryption();
		} catch (e) {
			error = msg(e);
		}
	}

	async function run(fn: () => Promise<Encryption>) {
		busy = true;
		error = '';
		try {
			enc = await fn();
			pass = confirm = '';
			acknowledged = false;
			confirmDisable = false;
		} catch (e) {
			error = msg(e);
		}
		busy = false;
	}

	// A new key (nothing in the store yet, or we could not tell) needs the
	// passphrase twice and the warning acknowledged. Joining an existing key
	// only needs the passphrase.
	const creating = $derived(enc?.keyInStore !== true);

	onMount(load);
</script>

{#if error}<p class="bad">{error}</p>{/if}

{#if !enc}
	<p class="muted">Loading…</p>
{:else if enc.enabled && enc.unlocked}
	<section class="card">
		<h2>Encryption is on</h2>
		<p class="ok">Your worlds are encrypted on this computer before they are uploaded.</p>
		<p class="muted small">
			Your cloud provider only sees scrambled data. Other computers need the same passphrase to
			sync these worlds.
			{#if enc.remember}This computer remembers the key in the system keychain.{:else}This computer
				asks for the passphrase again after WorldHop restarts.{/if}
		</p>
		{#if !enc.pro}
			<p class="warn small">
				Your Pro license isn’t active. Existing encrypted worlds keep syncing, but you can’t turn
				encryption on again once it’s off.
			</p>
		{/if}
		<div class="row">
			<button class="secondary" disabled={busy} onclick={() => run(lockEncryption)}>Lock now</button>
			{#if confirmDisable}
				<button class="danger" disabled={busy} onclick={() => run(disableEncryption)}>
					Turn off on this computer
				</button>
				<button class="secondary" onclick={() => (confirmDisable = false)}>Cancel</button>
			{:else}
				<button class="secondary" onclick={() => (confirmDisable = true)}>Turn off…</button>
			{/if}
		</div>
		{#if confirmDisable}
			<p class="muted small">
				This computer goes back to uploading worlds unencrypted. Encrypted copies already in your
				cloud stay encrypted, and stay readable with the passphrase.
			</p>
		{/if}
	</section>
{:else if enc.enabled}
	<section class="card">
		<h2>Encryption is locked</h2>
		<p class="warn">
			Syncing is paused until you enter your passphrase. Nothing is uploaded unencrypted while it
			is locked.
		</p>
		<form
			class="col"
			onsubmit={(e) => {
				e.preventDefault();
				run(() => unlockEncryption(pass, remember));
			}}>
			<input type="password" bind:value={pass} placeholder="Passphrase" autocomplete="off" />
			<label class="check">
				<input type="checkbox" bind:checked={remember} /> Remember on this computer (system keychain)
			</label>
			<div><button type="submit" disabled={busy || !pass}>{busy ? 'Checking…' : 'Unlock'}</button></div>
		</form>
	</section>
{:else}
	<section class="card">
		<h2>Encrypt my worlds <span class="badge">Pro</span></h2>
		<p class="muted">
			Worlds are encrypted on this computer before they reach your cloud account, so even your
			cloud provider can’t read them. File names, folder layout and world names are hidden too.
		</p>

		{#if !enc.pro}
			<p class="warn">Encryption is part of WorldHop Pro. <a href="/license">Activate a license</a> to turn it on.</p>
		{:else}
			{#if enc.keyInStore === true}
				<p class="ok">
					Encryption is already set up for this cloud account. Enter the same passphrase you chose
					on your other computer.
				</p>
			{/if}
			<form
				class="col"
				onsubmit={(e) => {
					e.preventDefault();
					run(() => enableEncryption({ passphrase: pass, confirm, acknowledged, remember }));
				}}>
				<input type="password" bind:value={pass} placeholder="Passphrase" autocomplete="new-password" />
				{#if creating}
					<input type="password" bind:value={confirm} placeholder="Type it again" autocomplete="new-password" />
					<p class="muted small">
						At least 10 characters. A few random words works well. Choose one you can remember, or
						keep it in a password manager.
					</p>
					<div class="danger-box">
						<strong>If you lose this passphrase, your encrypted worlds cannot be recovered.</strong>
						Nobody can reset it, not us and not your cloud provider. Keep a copy somewhere safe.
					</div>
					<label class="check">
						<input type="checkbox" bind:checked={acknowledged} /> I understand that a lost passphrase
						means lost data
					</label>
				{/if}
				<label class="check">
					<input type="checkbox" bind:checked={remember} /> Remember on this computer (system keychain)
				</label>
				<div>
					<button
						type="submit"
						disabled={busy || !pass || (creating && (!confirm || !acknowledged))}>
						{busy ? 'Setting up…' : 'Turn on encryption'}
					</button>
				</div>
			</form>
			{#if creating}
				<p class="muted small">
					Worlds already in your cloud account aren’t changed. After you turn this on, your worlds
					are uploaded again, encrypted. The older unencrypted copy stays in your cloud account
					until you remove it from your provider’s app settings (for Google Drive: Settings →
					Manage apps → WorldHop → Delete hidden app data). Turn encryption on for every computer
					that syncs these worlds, using the same passphrase.
				</p>
			{/if}
		{/if}
	</section>
{/if}

<style>
	.col { display: flex; flex-direction: column; gap: 10px; max-width: 420px; margin-top: 8px; }
	.row { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; margin-top: 8px; }
	.ok { color: var(--ok); }
	.bad { color: var(--bad); }
	.warn { color: var(--warn); }
	.check { display: flex; gap: 8px; align-items: flex-start; font-size: 0.92rem; }
	.danger-box {
		border: 1px solid var(--bad);
		background: #00000040;
		border-left-width: 6px;
		padding: 8px 14px;
		font-size: 0.92rem;
	}
	.badge {
		font-size: 0.8rem;
		background: var(--warn);
		color: #000;
		text-shadow: none;
		border: 2px solid #000;
		padding: 0 7px;
		vertical-align: middle;
	}
</style>
