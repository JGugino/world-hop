<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { sayBye } from '$lib/api';
	import { status } from '$lib/status.svelte';

	let { children } = $props();

	const tabs = [
		{ href: '/', label: 'Worlds' },
		{ href: '/sources', label: 'Sources' },
		{ href: '/cloud', label: 'Cloud account' }
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
	<div class="wrap bar">
		<div>
			<h1>WorldHop</h1>
			<p class="muted small">Keep your Minecraft worlds in sync between computers.</p>
		</div>
		<div class="syncbox">
			<button onclick={() => status.sync()} disabled={s?.state === 'syncing'}>
				{s?.state === 'syncing' ? 'Syncing…' : 'Force sync'}
			</button>
			<span class="muted small">Last sync: {s ? ago(s.lastSync) : '…'}</span>
		</div>
	</div>
	<nav class="wrap">
		{#each tabs as t}
			<a href={t.href} class:active={page.url.pathname === t.href}>{t.label}</a>
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
	:global(:root) {
		--bg: #15181c;
		--panel: #1f242a;
		--line: #2c333b;
		--text: #e8eaed;
		--muted: #9aa0a6;
		--accent: #3b82f6;
		--ok: #34d399;
		--warn: #fbbf24;
		--bad: #f87171;
	}
	:global(body) {
		margin: 0;
		font-family: system-ui, sans-serif;
		background: var(--bg);
		color: var(--text);
		line-height: 1.45;
	}
	:global(h2) { margin: 0 0 12px; font-size: 1.1rem; }
	:global(.muted) { color: var(--muted); }
	:global(.small) { font-size: 0.85rem; margin: 0; }
	:global(.card) {
		background: var(--panel);
		border: 1px solid var(--line);
		border-radius: 8px;
		padding: 12px 16px;
		margin: 12px 0;
	}
	:global(button) {
		background: var(--accent);
		color: #fff;
		border: 0;
		border-radius: 6px;
		padding: 7px 14px;
		cursor: pointer;
		font: inherit;
	}
	:global(button.secondary) { background: transparent; border: 1px solid var(--line); color: var(--text); }
	:global(button.danger) { background: transparent; border: 1px solid var(--bad); color: var(--bad); }
	:global(button:disabled) { opacity: 0.5; cursor: default; }
	:global(button:focus-visible, a:focus-visible, input:focus-visible) { outline: 2px solid var(--accent); outline-offset: 2px; }
	:global(input[type='text'], input[type='password']) {
		background: var(--bg);
		color: var(--text);
		border: 1px solid var(--line);
		border-radius: 6px;
		padding: 7px 10px;
		font: inherit;
	}
	:global(code) { font-size: 0.85rem; word-break: break-all; }
	.wrap { max-width: 760px; margin: 0 auto; padding: 0 16px; }
	header { border-bottom: 1px solid var(--line); padding-top: 20px; }
	.bar { display: flex; justify-content: space-between; align-items: center; gap: 16px; flex-wrap: wrap; }
	h1 { margin: 0; font-size: 1.5rem; }
	.syncbox { display: flex; align-items: center; gap: 12px; }
	nav { display: flex; gap: 4px; margin-top: 12px; }
	nav a {
		color: var(--muted);
		text-decoration: none;
		padding: 8px 12px;
		border-bottom: 2px solid transparent;
	}
	nav a.active { color: var(--text); border-color: var(--accent); }
	main { padding-bottom: 24px; }
	.banner { background: var(--panel); border-radius: 6px; padding: 8px 12px; margin: 12px 0 0; font-size: 0.9rem; }
	.banner.warn { border-left: 3px solid var(--warn); }
	.banner.err { border-left: 3px solid var(--bad); color: var(--bad); }
	footer { color: #6b7280; font-size: 0.8rem; margin-top: 32px; }
</style>
