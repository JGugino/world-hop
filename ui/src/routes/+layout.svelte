<script lang="ts">
	import '$lib/theme.css';
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { sayBye } from '$lib/api';
	import { status } from '$lib/status.svelte';

	let { children } = $props();

	const tabs = [
		{ href: '/', label: 'Worlds' },
		{ href: '/sources', label: 'Sources' },
		{ href: '/cloud', label: 'Cloud account' },
		{ href: '/encryption', label: 'Encryption' },
		{ href: '/license', label: 'License' }
	];

	function ago(iso: string | null) {
		if (!iso) return 'never';
		const s = Math.max(0, Math.round((Date.now() - new Date(iso).getTime()) / 1000));
		if (s < 60) return 'just now';
		if (s < 3600) return `${Math.floor(s / 60)} min ago`;
		if (s < 86400) return `${Math.floor(s / 3600)} h ago`;
		return new Date(iso).toLocaleDateString();
	}

	onMount(() => {
		status.refresh();
		const t = setInterval(() => status.refresh(), 3000);
		window.addEventListener('pagehide', sayBye);
		return () => {
			clearInterval(t);
			window.removeEventListener('pagehide', sayBye);
		};
	});

	const s = $derived(status.current);
</script>

<header>
	<div class="grass" aria-hidden="true"></div>
	<div class="wrap bar">
		<div class="brand">
			<img src="/favicon.svg" alt="" width="56" height="56" />
			<div>
				<h1>WorldHop</h1>
				<p class="tag">Keep your Minecraft worlds in sync between computers.</p>
			</div>
		</div>
		<div class="syncbox">
			<button onclick={() => status.sync()} disabled={s?.state === 'syncing'}>
				{s?.state === 'syncing' ? 'Syncing…' : 'Force sync'}
			</button>
			<span class="last">Last sync: {s ? ago(s.lastSync) : '…'}</span>
		</div>
	</div>
	<nav class="wrap" aria-label="Sections">
		{#each tabs as t}
			<a href={t.href} class:active={page.url.pathname === t.href} aria-current={page.url.pathname === t.href ? 'page' : undefined}>{t.label}</a>
		{/each}
	</nav>
</header>

<main class="wrap">
	{#if status.error}<p class="banner err">Can't reach WorldHop: {status.error}</p>{/if}
	{#if s}
		<p class="banner">{s.lastResult}</p>
		{#each s.warnings ?? [] as w}<p class="banner warn">{w}</p>{/each}
	{/if}
	{@render children()}
	<footer>
		WorldHop {s?.version ?? ''} is not an official Minecraft product and is not affiliated with Mojang
		or Microsoft.
	</footer>
</main>

<style>
	.wrap { max-width: 800px; margin: 0 auto; padding: 0 16px; }
	header { background: #14171a; border-bottom: 2px solid #000; }
	.grass {
		height: 10px;
		background: var(--grass);
		background-size: 128px 128px;
		border-bottom: 2px solid #000;
		box-shadow: inset 0 -4px 0 #2f6b21;
	}
	.bar { display: flex; justify-content: space-between; align-items: center; gap: 16px; flex-wrap: wrap; padding-top: 16px; padding-bottom: 6px; }
	.brand { display: flex; align-items: center; gap: 12px; min-width: 0; }
	.brand img { flex: none; image-rendering: pixelated; }
	h1 { margin: 0; font-size: 1.05rem; color: #fff; }
	h1::first-letter { color: var(--accent); }
	.tag { margin: 2px 0 0; font-size: 0.88rem; color: var(--muted); }
	.syncbox { display: flex; align-items: center; gap: 12px; }
	.last { color: var(--muted); font-size: 0.88rem; }
	nav { display: flex; gap: 2px; margin-top: 8px; flex-wrap: wrap; }
	nav a {
		color: var(--muted);
		text-decoration: none;
		padding: 8px 14px 9px;
		border-bottom: 4px solid transparent;
	}
	nav a:hover { color: #fff; background: #ffffff10; }
	nav a.active { color: #fff; border-bottom-color: var(--accent); }
	main { padding-bottom: 24px; }
	.banner {
		background: var(--panel); border: 2px solid #000; box-shadow: inset 2px 2px 0 var(--panel-hi), inset -2px -2px 0 var(--panel-lo);
		padding: 8px 12px; margin: 14px 0 0; font-size: 0.92rem;
	}
	.banner.warn { border-left: 6px solid var(--warn); }
	.banner.err { border-left: 6px solid var(--bad); color: var(--bad); }
	footer { color: #7d858d; font-size: 0.8rem; margin-top: 32px; }
</style>
