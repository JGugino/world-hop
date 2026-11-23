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
	header { background: #00000066; border-bottom: 4px solid #000; }
	.grass {
		height: 24px;
		background: var(--grass);
		background-size: 128px 128px;
		border-bottom: 4px solid #000;
		box-shadow: inset 0 -8px 0 #2f6b21;
		/* ragged pixel edge where the grass meets the dirt */
		position: relative;
	}
	.grass::after {
		content: '';
		position: absolute;
		left: 0; right: 0; top: 100%;
		height: 8px;
		background: repeating-linear-gradient(90deg, #2f6b21 0 16px, transparent 16px 40px, #2f6b21 40px 56px, transparent 56px 96px, #2f6b21 96px 112px, transparent 112px 144px);
		margin-top: 4px;
	}
	.bar { display: flex; justify-content: space-between; align-items: center; gap: 16px; flex-wrap: wrap; padding-top: 20px; padding-bottom: 8px; }
	.brand { display: flex; align-items: center; gap: 14px; min-width: 0; }
	.brand img { flex: none; image-rendering: pixelated; filter: drop-shadow(4px 4px 0 #000a); }
	h1 { margin: 0; font-size: 1.35rem; color: #fff; text-shadow: 4px 4px 0 #000, 0 0 0 #000; }
	h1::first-letter { color: var(--accent); }
	.tag { margin: 4px 0 0; font-size: 1.1rem; color: #e8e0d4; text-shadow: 2px 2px 0 #000; }
	.syncbox { display: flex; align-items: center; gap: 14px; }
	.last { color: #fff; text-shadow: 2px 2px 0 #000; }
	nav { display: flex; gap: 6px; margin-top: 10px; flex-wrap: wrap; }
	nav a {
		color: #fff;
		text-decoration: none;
		font-size: 1.3rem;
		padding: 8px 16px 6px;
		background: #4a4a4f var(--stone);
		background-size: 64px 64px;
		border: 3px solid #000;
		border-bottom: 0;
		box-shadow: inset 3px 3px 0 #6b6b71, inset -3px 0 0 #2a2a2d;
		text-shadow: 2px 2px 0 #000a;
		margin-bottom: -4px;
		padding-bottom: 8px;
	}
	nav a:hover { background-color: #66666c; }
	nav a.active {
		background: #3a3a3d var(--stone);
		background-size: 64px 64px;
		box-shadow: inset 3px 3px 0 #6b6b71, inset -3px 0 0 #2a2a2d, inset 0 6px 0 var(--accent);
		padding-top: 14px;
		color: #fff;
	}
	main { padding-bottom: 28px; }
	.banner {
		background: #1b1b1d; border: 4px solid #000;
		box-shadow: inset 3px 3px 0 #000, inset -3px -3px 0 #4e4e53;
		padding: 10px 14px; margin: 18px 0 0; font-size: 1.25rem;
	}
	.banner.warn { border-left-color: var(--warn); color: var(--warn); }
	.banner.err { border-left-color: var(--bad); color: var(--bad); }
	footer { color: #e8e0d4; text-shadow: 2px 2px 0 #000; font-size: 1.1rem; margin-top: 36px; }
</style>
