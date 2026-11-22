<script lang="ts">
	import { onMount } from 'svelte';
	import { getProviders, linkProvider, unlinkProvider, type Provider } from '$lib/api';

	let providers = $state<Provider[] | null>(null);
	let error = $state('');
	let busy = $state<string | null>(null);
	let confirmUnlink = $state<string | null>(null);

	async function load() {
		try {
			providers = await getProviders();
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

{#if providers === null}
	<p class="muted">Loading…</p>
{:else}
	{#each providers as p (p.id)}
		<section class="card">
			<div class="head">
				<div>
					<h2>{p.name}</h2>
					{#if p.linked}
						<p class="ok small">Connected{p.account ? ` as ${p.account}` : ''}</p>
					{:else if p.pending}
						<p class="muted small">Waiting for you to approve access in your browser…</p>
					{:else if !p.available}
						<p class="muted small">Coming soon.</p>
					{:else}
						<p class="muted small">Not connected.</p>
					{/if}
					{#if p.error}<p class="bad small">{p.error}</p>{/if}
				</div>
				{#if p.linked}
					{#if confirmUnlink === p.id}
						<span class="row">
							<button class="danger" disabled={busy === p.id} onclick={() => disconnect(p)}>Disconnect</button>
							<button class="secondary" onclick={() => (confirmUnlink = null)}>Cancel</button>
						</span>
					{:else}
						<button class="secondary" onclick={() => (confirmUnlink = p.id)}>Disconnect</button>
					{/if}
				{:else}
					<button disabled={!p.available || p.pending || busy === p.id} onclick={() => connect(p)}>
						{p.error ? 'Reconnect' : 'Connect'}
					</button>
				{/if}
			</div>
			{#if confirmUnlink === p.id}
				<p class="muted small">
					This forgets the saved sign-in on this computer. Worlds already in your cloud account stay
					where they are.
				</p>
			{/if}
		</section>
	{/each}
{/if}

<style>
	.head { display: flex; justify-content: space-between; align-items: center; gap: 12px; }
	.head h2 { margin-bottom: 4px; }
	.row { display: flex; gap: 8px; }
	.ok { color: var(--ok); margin: 0; }
	.bad { color: var(--bad); }
	p.small { margin: 0; }
</style>
