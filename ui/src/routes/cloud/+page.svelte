<script lang="ts">
	import { onMount } from 'svelte';
	import { getProviders, linkProvider, unlinkProvider, type Provider } from '$lib/api';

	let p = $state<Provider | null>(null);
	let loaded = $state(false);
	let error = $state('');
	let busy = $state<string | null>(null);
	let confirmUnlink = $state<string | null>(null);

	async function load() {
		try {
			// One provider today (Google Drive); the API stays a list so more can be added.
			p = (await getProviders())[0] ?? null;
			loaded = true;
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}

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

	async function disconnect(p: Provider) {
		busy = p.id;
		error = '';
		try {
			await unlinkProvider(p.id);
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
		busy = null;
		confirmUnlink = null;
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
{:else if p}
	{@const prov = p}
	<section class="card">
		<div class="head">
			<div>
				<h2>{prov.name}</h2>
				{#if prov.linked}
					<p class="ok small">Connected{prov.account ? ` as ${prov.account}` : ''}</p>
				{:else if prov.pending}
					<p class="muted small">Waiting for you to approve access in your browser…</p>
				{:else if !prov.available}
					<p class="muted small">Coming soon.</p>
				{:else}
					<p class="muted small">Not connected.</p>
				{/if}
				{#if prov.error}<p class="bad small">{prov.error}</p>{/if}
			</div>
			{#if prov.linked}
				{#if confirmUnlink === prov.id}
					<span class="row">
						<button class="danger" disabled={busy === prov.id} onclick={() => disconnect(prov)}>Disconnect</button>
						<button class="secondary" onclick={() => (confirmUnlink = null)}>Cancel</button>
					</span>
				{:else}
					<button class="secondary" onclick={() => (confirmUnlink = prov.id)}>Disconnect</button>
				{/if}
			{:else}
				<button disabled={!prov.available || prov.pending || busy === prov.id} onclick={() => connect(prov)}>
					{prov.error ? 'Reconnect' : 'Connect'}
				</button>
			{/if}
		</div>
		{#if confirmUnlink === prov.id}
			<p class="muted small">
				This forgets the saved sign-in on this computer. Worlds already in your cloud account stay
				where they are.
			</p>
		{/if}
	</section>
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
</style>
