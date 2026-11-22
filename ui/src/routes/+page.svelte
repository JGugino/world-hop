<script lang="ts">
	import { onMount } from 'svelte';
	import { getStatus, forceSync, sayBye, type Status } from '$lib/api';

	let status = $state<Status | null>(null);
	let error = $state('');

	async function refresh() {
		try {
			status = await getStatus();
			error = '';
		} catch (e) {
			error = String(e);
		}
	}

	async function sync() {
		try {
			status = await forceSync();
		} catch (e) {
			error = String(e);
		}
	}

	onMount(() => {
		refresh();
		const t = setInterval(refresh, 3000);
		window.addEventListener('pagehide', sayBye);
		return () => {
			clearInterval(t);
			window.removeEventListener('pagehide', sayBye);
		};
	});
</script>

<main>
	<h1>WorldHop</h1>
	<p class="sub">Keep your Minecraft worlds in sync between computers.</p>

	{#if error}<p class="err">{error}</p>{/if}

	<section>
		<h2>Status</h2>
		{#if status}
			<p>State: <b>{status.state}</b></p>
			<p>Last sync: {status.lastSync ?? 'never'}</p>
			<p>{status.lastResult}</p>
			<button onclick={sync} disabled={status.state === 'syncing'}>Force sync</button>
		{:else}
			<p>Loading…</p>
		{/if}
	</section>

	<section>
		<h2>Cloud account</h2>
		<p class="muted">Connect Google Drive or Dropbox — coming soon.</p>
	</section>

	<section>
		<h2>Worlds</h2>
		<p class="muted">World picker — coming soon.</p>
	</section>

	<footer>WorldHop is not an official Minecraft product and is not affiliated with Mojang or Microsoft.</footer>
</main>

<style>
	:global(body) { margin: 0; font-family: system-ui, sans-serif; background: #15181c; color: #e8eaed; }
	main { max-width: 640px; margin: 0 auto; padding: 24px 16px; }
	h1 { margin: 0; }
	.sub, .muted { color: #9aa0a6; }
	section { background: #1f242a; border-radius: 8px; padding: 12px 16px; margin: 16px 0; }
	h2 { margin: 0 0 8px; font-size: 1.1rem; }
	button { background: #3b82f6; color: #fff; border: 0; border-radius: 6px; padding: 8px 14px; cursor: pointer; }
	button:disabled { opacity: 0.5; cursor: default; }
	.err { color: #f87171; }
	footer { color: #6b7280; font-size: 0.8rem; margin-top: 32px; }
</style>
