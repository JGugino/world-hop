<script lang="ts">
	import { onMount } from 'svelte';
	import {
		getEncryption,
		enableEncryption,
		unlockEncryption,
		lockEncryption,
		disableEncryption,
		changePassphrase,
		cancelChangePassphrase,
		dismissChangePassphrase,
		restoreKeyHeader,
		acceptKeyRollback,
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

	// Changing the passphrase.
	let changing = $state(false);
	let curPass = $state('');
	let newPass = $state('');
	let newConfirm = $state('');
	let newAck = $state(false);
	let newRemember = $state(false);
	let confirmCancel = $state(false);
	let confirmAccept = $state(false);

	const rekey = $derived(enc?.rekey);
	const running = $derived(rekey?.state === 'running');
	const pct = $derived(
		rekey && rekey.total > 0 ? Math.min(100, Math.round((rekey.done / rekey.total) * 100)) : 0
	);
	const newTooShort = $derived(newPass.length > 0 && [...newPass].length < 10);

	function openChange() {
		changing = true;
		newRemember = enc?.remember ?? false;
	}
	function closeChange() {
		changing = false;
		curPass = newPass = newConfirm = '';
		newAck = false;
	}
	async function startChange() {
		busy = true;
		error = '';
		try {
			enc = await changePassphrase({
				current: curPass,
				passphrase: newPass,
				confirm: newConfirm,
				acknowledged: newAck,
				remember: newRemember
			});
			closeChange();
		} catch (e) {
			error = msg(e);
		}
		busy = false;
	}

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

	onMount(() => {
		load();
		// While a change runs, follow its progress.
		const t = setInterval(() => {
			if (running) load();
		}, 1000);
		return () => clearInterval(t);
	});
</script>

{#if error}<p class="bad">{error}</p>{/if}

{#if enc?.keyRollback}
	<section class="card fail" aria-live="polite">
		<h2>Your cloud’s encryption key looks rolled back</h2>
		<p class="bad">
			The encryption key in your cloud account is older than the one this computer has used, or it is
			gone. Syncing is stopped. Something removed or rolled back that part of your cloud.
		</p>
		<p class="muted small">
			WorldHop has changed nothing and won’t until you choose. The key information holds no secret, and this
			computer kept a copy of it.
		</p>
		<div class="row">
			<button disabled={busy} onclick={() => run(restoreKeyHeader)}>Put the key back from this computer</button>
			{#if confirmAccept}
				<button class="danger" disabled={busy} onclick={() => run(acceptKeyRollback)}>Yes, go by the cloud</button>
				<button class="secondary" onclick={() => (confirmAccept = false)}>Cancel</button>
			{:else}
				<button class="secondary" disabled={busy} onclick={() => (confirmAccept = true)}>Go by what the cloud shows…</button>
			{/if}
		</div>
		<ul class="small muted">
			<li><b>Put the key back:</b> writes this computer’s saved copy of the newest key back to your cloud. Nothing else is touched.</li>
			<li>
				<b>Go by what the cloud shows:</b> this computer forgets its newer key and uses whatever the cloud holds now, and you
				enter a passphrase again. Data written under the newer key stays in the cloud but can’t be read here. If the cloud has no key at
				all, encryption is turned off on this computer so you can set up a new one.
			</li>
		</ul>
	</section>
{/if}

{#if rekey}
	<section class="card" class:fail={rekey.state === 'failed'} aria-live="polite">
		{#if rekey.state === 'running'}
			<h2>Changing your passphrase</h2>
			<p class="muted">
				{#if rekey.phase === 'finishing'}Switching over…{:else if rekey.total === 0}Looking at your
					worlds…{:else}Re-encrypting {rekey.worlds} world{rekey.worlds === 1 ? '' : 's'} with the
					new passphrase.{/if}
			</p>
			<div class="track" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow={pct}>
				<div class="fill" style="width: {pct}%"></div>
			</div>
			<p class="small muted">{rekey.done} of {rekey.total} items · {pct}%</p>
			<p class="muted small">
				Syncing is paused on this computer until this finishes. Keep WorldHop open, and avoid
				syncing from your other computers meanwhile. If you stop it, nothing changes.
			</p>
			<div class="row">
				{#if confirmCancel}
					<button class="danger" onclick={() => run(cancelChangePassphrase)}>Stop the change</button>
					<button class="secondary" onclick={() => (confirmCancel = false)}>Keep going</button>
				{:else}
					<button class="secondary" onclick={() => (confirmCancel = true)}>Cancel…</button>
				{/if}
			</div>
		{:else if rekey.state === 'done'}
			<h2>Passphrase changed</h2>
			<p class="ok">
				Your worlds are now encrypted with the new passphrase, and syncing continues as normal.
			</p>
			<ul class="small muted">
				<li>
					On your other computers, open the Encryption tab and enter the <strong>new</strong>
					passphrase when asked. The old one no longer works for new data.
				</li>
				<li>
					The previous encrypted copy is still in your cloud account, and can still be opened with
					the <strong>old</strong> passphrase. WorldHop can’t delete it for you. If you changed the
					passphrase because the old one was exposed, remove the old copy in your cloud provider’s
					own settings.
				</li>
			</ul>
			<div class="row"><button onclick={() => run(dismissChangePassphrase)}>OK</button></div>
		{:else if rekey.state === 'cancelled'}
			<h2>Change stopped</h2>
			<p class="muted">
				Nothing was changed. Your old passphrase still works, and syncing continues. Starting
				again with the same new passphrase picks up where this left off.
			</p>
			<div class="row"><button onclick={() => run(dismissChangePassphrase)}>OK</button></div>
		{:else}
			<h2>The passphrase was not changed</h2>
			<p class="bad">{rekey.error}</p>
			<p class="muted small">
				Nothing was changed. Your old passphrase still works, and syncing continues. You can try
				again.
			</p>
			<div class="row"><button onclick={() => run(dismissChangePassphrase)}>OK</button></div>
		{/if}
	</section>
{/if}

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
			<button class="secondary" disabled={busy || running} onclick={() => run(lockEncryption)}>Lock now</button>
			<button class="secondary" disabled={busy || running || changing} onclick={openChange}>
				Change passphrase…
			</button>
			{#if confirmDisable}
				<button class="danger" disabled={busy} onclick={() => run(disableEncryption)}>
					Turn off on this computer
				</button>
				<button class="secondary" onclick={() => (confirmDisable = false)}>Cancel</button>
			{:else}
				<button class="secondary" onclick={() => (confirmDisable = true)}>Turn off…</button>
			{/if}
		</div>
		{#if changing}
			<form
				class="col change"
				onsubmit={(e) => {
					e.preventDefault();
					startChange();
				}}>
				<h3>Change passphrase</h3>
				<input type="password" bind:value={curPass} placeholder="Current passphrase" autocomplete="current-password" />
				<input type="password" bind:value={newPass} placeholder="New passphrase" autocomplete="new-password" />
				{#if newTooShort}<p class="warn small">At least 10 characters.</p>{/if}
				<input type="password" bind:value={newConfirm} placeholder="New passphrase again" autocomplete="new-password" />
				<div class="danger-box">
					<strong>If you lose the new passphrase, your encrypted worlds cannot be recovered.</strong>
					Nobody can reset it, not us and not your cloud provider.
				</div>
				<label class="check">
					<input type="checkbox" bind:checked={newAck} /> I understand that a lost passphrase means
					lost data
				</label>
				<label class="check">
					<input type="checkbox" bind:checked={newRemember} /> Remember on this computer (system keychain)
				</label>
				<p class="muted small">
					Every world is re-encrypted with the new passphrase and uploaded again. That can take a
					while and uses as much upload as your worlds are big. Syncing here pauses until it is done.
					Your other computers keep working until it finishes, then ask for the new passphrase. The
					previous encrypted copy stays in your cloud account (WorldHop can’t delete it) and can still
					be opened with the old passphrase.
				</p>
				<div class="row">
					<button
						type="submit"
						disabled={busy || !curPass || !newPass || newTooShort || !newConfirm || !newAck}>
						{busy ? 'Checking…' : 'Change passphrase'}
					</button>
					<button type="button" class="secondary" onclick={closeChange}>Cancel</button>
				</div>
			</form>
		{/if}
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
		{#if enc.keyChanged}
			<p class="warn">
				The passphrase was changed on another computer. Enter the <strong>new</strong> passphrase to
				resume syncing. The old one no longer works.
			</p>
		{:else}
			<p class="warn">
				Syncing is paused until you enter your passphrase. Nothing is uploaded unencrypted while it
				is locked.
			</p>
		{/if}
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
	.change { border-top: 2px solid var(--line); margin-top: 16px; padding-top: 12px; max-width: 460px; }
	.card.fail { border-left: 6px solid var(--bad); }
	.track {
		height: 22px; background: #111315; border: 2px solid #000; margin: 10px 0 6px;
		box-shadow: inset 2px 2px 0 #000, inset -2px -2px 0 #3c434a; padding: 2px;
	}
	.fill {
		height: 100%; background: var(--accent); box-shadow: inset 0 -4px 0 #2f6b21, inset 0 3px 0 #8ee070;
		transition: width 0.4s steps(12);
	}
	ul { margin: 8px 0; padding-left: 20px; }
	li { margin: 6px 0; }
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
		padding: 3px 9px;
		line-height: 1.3;
		vertical-align: middle;
		margin-left: 8px;
	}
</style>
