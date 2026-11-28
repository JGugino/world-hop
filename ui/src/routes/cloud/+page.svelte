<!--
The Cloud account tab. Shows each storage choice as a card: Google Drive (connect
with a browser sign-in) and S3-compatible storage (a Pro feature, set up with a
form). Connecting, disconnecting and the S3 form all live here. The page polls every
2 seconds so a card flips to Connected as soon as the sign-in finishes in the other
tab.
-->
<script lang="ts">
	import { onMount } from 'svelte';
	import {
		getProviders,
		linkProvider,
		unlinkProvider,
		getS3,
		saveS3,
		testS3,
		type Provider,
		type S3Input
	} from '$lib/api';

	let providers = $state<Provider[]>([]);
	let loaded = $state(false);
	let error = $state('');
	// busy holds the provider whose action is running.
	let busy = $state<string | null>(null);
	let confirmUnlink = $state<string | null>(null);

	// load fetches the providers and their state.
	async function load() {
		try {
			providers = await getProviders();
			loaded = true;
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}

	// connect starts linking a provider. For Google Drive the program opens the browser
	// itself; if it didn't, the sign-in URL is opened in a new tab. The page then polls
	// (see onMount) until the account shows as connected.
	async function connect(p: Provider) {
		busy = p.id;
		error = '';
		try {
			const { url } = await linkProvider(p.id);
			if (url) window.open(url, '_blank', 'noopener');
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
		busy = null;
		await load();
	}

	// disconnect forgets the stored sign-in or keys, and the server also asks the
	// provider to end WorldHop's access. If that could not be done (offline) the
	// sign-in is still forgotten, and the page says how to finish by hand. Cloud data
	// is left alone.
	async function disconnect(p: Provider) {
		busy = p.id;
		error = '';
		try {
			const res = await unlinkProvider(p.id);
			if (res.warning) error = res.warning;
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
		busy = null;
		confirmUnlink = null;
		await load();
	}

	// --- S3-compatible storage (Pro) ---
	let editingS3 = $state(false);
	let s3 = $state<S3Input>({
		endpoint: '', region: '', bucket: '', prefix: '', pathStyle: false, accessKey: '', secretKey: ''
	});
	let s3HasSecret = $state(false);
	let s3Result = $state<{ ok: boolean; text: string } | null>(null);
	let s3Busy = $state(false);

	// editS3 opens the S3 form filled with the saved settings (never the secret key;
	// leaving it blank keeps the stored one).
	async function editS3() {
		error = '';
		s3Result = null;
		try {
			const { hasSecret, ...c } = await getS3();
			s3 = { ...c, secretKey: '' };
			s3HasSecret = hasSecret;
			editingS3 = true;
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}

	// runTestS3 checks the entered settings against the bucket without saving them.
	async function runTestS3() {
		s3Busy = true;
		s3Result = null;
		try {
			const r = await testS3(s3);
			s3Result = r.ok
				? { ok: true, text: 'Connected. This key can reach the bucket.' }
				: { ok: false, text: r.error ?? 'Could not connect.' };
		} catch (e) {
			s3Result = { ok: false, text: e instanceof Error ? e.message : String(e) };
		}
		s3Busy = false;
	}

	// doSaveS3 saves the S3 settings and keys and makes S3 the storage in use.
	async function doSaveS3() {
		s3Busy = true;
		error = '';
		try {
			await saveS3(s3);
			s3.secretKey = '';
			editingS3 = false;
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
		s3Busy = false;
		await load();
	}

	onMount(() => {
		load();
		// Poll so the card flips to "Connected" once the browser step finishes.
		const t = setInterval(load, 2000);
		return () => clearInterval(t);
	});
</script>

<p class="muted">
	Your worlds are stored in your own cloud account. WorldHop never sees or hosts them.
</p>
{#if error}<p class="bad">{error}</p>{/if}

{#if !loaded}
	<p class="muted">Loading…</p>
{:else if providers.length}
	{#each providers as prov (prov.id)}
		<section class="card">
			<div class="head">
				<div>
					<h2>
						{prov.name}
						{#if prov.pro}<span class="badge">Pro</span>{/if}
					</h2>
					{#if prov.locked}
						<p class="muted small">
							Store worlds in your own S3, Cloudflare R2, Backblaze B2 or MinIO bucket. Needs a
							WorldHop Pro licence.
						</p>
					{:else if prov.linked}
						<p class="ok small">Connected{prov.account ? ` as ${prov.account}` : ''}</p>
					{:else if prov.pending}
						<p class="muted small">Waiting for you to approve access in your browser…</p>
					{:else if !prov.available}
						<p class="bad small">{prov.note ?? 'Not available in this build.'}</p>
					{:else}
						<p class="muted small">Not connected.</p>
					{/if}
					{#if prov.error}<p class="bad small">{prov.error}</p>{/if}
				</div>
				{#if prov.locked}
					<!-- nothing to do until a licence is present -->
				{:else if prov.linked}
					{#if confirmUnlink === prov.id}
						<span class="row">
							<button class="danger" disabled={busy === prov.id} onclick={() => disconnect(prov)}>Disconnect</button>
							<button class="secondary" onclick={() => (confirmUnlink = null)}>Cancel</button>
						</span>
					{:else}
						<span class="row">
							{#if prov.id === 's3' && !editingS3}
								<button class="secondary" onclick={editS3}>Edit</button>
							{/if}
							<button class="secondary" onclick={() => (confirmUnlink = prov.id)}>Disconnect</button>
						</span>
					{/if}
				{:else if prov.id === 's3'}
					{#if !editingS3}
						<button disabled={!prov.available} onclick={editS3}>Set up</button>
					{/if}
				{:else}
					<button disabled={!prov.available || prov.pending || busy === prov.id} onclick={() => connect(prov)}>
						{prov.error ? 'Reconnect' : 'Connect'}
					</button>
				{/if}
			</div>
			{#if confirmUnlink === prov.id}
				<p class="muted small">
					{#if prov.id === 's3'}
						This removes the saved settings and the secret key from this computer. Worlds already
						in the bucket stay where they are.
					{:else}
						This forgets the saved sign-in on this computer. Worlds already in your cloud account
						stay where they are.
					{/if}
				</p>
			{/if}

			{#if prov.id === 's3' && editingS3 && !prov.locked}
				<form
					class="s3form"
					onsubmit={(e) => {
						e.preventDefault();
						doSaveS3();
					}}
				>
					<label>
						Endpoint
						<input type="text" bind:value={s3.endpoint} placeholder="Leave empty for AWS S3" autocomplete="off" />
						<span class="hint">e.g. https://&lt;account&gt;.r2.cloudflarestorage.com or http://localhost:9000</span>
					</label>
					<div class="two">
						<label>
							Bucket
							<input type="text" bind:value={s3.bucket} required autocomplete="off" />
						</label>
						<label>
							Region
							<input type="text" bind:value={s3.region} placeholder="us-east-1" autocomplete="off" />
						</label>
					</div>
					<label>
						Folder in the bucket (optional)
						<input type="text" bind:value={s3.prefix} placeholder="worldhop" autocomplete="off" />
					</label>
					<label class="check">
						<input type="checkbox" bind:checked={s3.pathStyle} />
						Use path-style addresses (needed by MinIO and most self-hosted servers)
					</label>
					<div class="two">
						<label>
							Access key
							<input type="text" bind:value={s3.accessKey} required autocomplete="off" />
						</label>
						<label>
							Secret key
							<input
								type="password"
								bind:value={s3.secretKey}
								placeholder={s3HasSecret ? 'Saved. Leave empty to keep it.' : ''}
								required={!s3HasSecret}
								autocomplete="new-password"
							/>
						</label>
					</div>
					<p class="muted small">The secret key is kept in your system keychain, not in a file.</p>
					{#if s3Result}
						<p class={s3Result.ok ? 'ok small' : 'bad small'} role="status">{s3Result.text}</p>
					{/if}
					<div class="row">
						<button type="button" class="secondary" disabled={s3Busy} onclick={runTestS3}>
							{s3Busy ? 'Working…' : 'Test connection'}
						</button>
						<button type="submit" disabled={s3Busy}>Save</button>
						<button type="button" class="secondary" onclick={() => (editingS3 = false)}>Cancel</button>
					</div>
				</form>
			{/if}
		</section>
	{/each}
{:else}
	<p class="muted">No cloud storage is available in this build.</p>
{/if}

<style>
	.head { display: flex; justify-content: space-between; align-items: center; gap: 12px; }
	.head h2 { margin-bottom: 4px; }
	.row { display: flex; gap: 8px; }
	.ok { color: var(--ok); margin: 0; }
	.bad { color: var(--bad); }
	p.small { margin: 0; }
	.badge { font-size: 0.8rem; padding: 3px 9px; line-height: 1.3; background: var(--warn); color: #000; text-shadow: none; border: 2px solid #000; vertical-align: middle; margin-left: 8px; }
	.s3form { display: grid; gap: 12px; margin-top: 16px; }
	.s3form label { display: grid; gap: 4px; font-size: 0.9rem; }
	.s3form label.check { display: flex; gap: 8px; align-items: center; }
	.s3form input[type='text'], .s3form input[type='password'] { width: 100%; box-sizing: border-box; }
	.two { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }
	.hint { color: var(--muted); font-size: 0.8rem; }
	@media (max-width: 560px) { .two { grid-template-columns: 1fr; } }
</style>
