<script lang="ts">
	import type { Status, Problem } from './api';
	import { ago } from './status.svelte';

	let { status }: { status: Status } = $props();

	const fixLabel: Record<string, string> = {
		'/cloud': 'Open Cloud account',
		'/license': 'Open License',
		'/encryption': 'Open Encryption',
		'/sources': 'Open Saves'
	};
	const phase = {
		checking: 'Checking for changes',
		uploading: 'Uploading to the cloud',
		downloading: 'Downloading from the cloud'
	};

	const syncing = $derived(status.state === 'syncing');
	const problems = $derived(status.problems ?? []);
	const tone = $derived(
		syncing
			? 'busy'
			: status.outcome === 'error'
				? 'error'
				: status.outcome === 'attention'
					? 'warn'
					: status.outcome === 'waiting'
						? 'info'
						: status.outcome === 'ok'
							? 'ok'
							: 'idle'
	);
	const headline = $derived(
		syncing
			? 'Syncing your worlds'
			: ({
					error: 'Sync ran into a problem',
					attention: 'Needs your attention',
					waiting: 'Waiting for Minecraft to close',
					ok: 'Everything is in sync',
					'': 'Not synced yet'
				} as Record<string, string>)[status.outcome]
	);
	const p = $derived(status.progress);
	const pct = $derived(p ? Math.min(100, (p.done / Math.max(p.total, 1)) * 100) : 0);

	function names(pr: Problem) {
		return (pr.worlds ?? []).join(', ');
	}
</script>

<section class="status {tone}" aria-live="polite">
	<div class="row">
		<span class="gem" aria-hidden="true"></span>
		<div class="text">
			<h2>{headline}</h2>
			{#if syncing && p}
				<p class="sub">
					{phase[p.action]}{#if p.world}: <strong>{p.world}</strong>{/if}
					{#if p.total > 1}<span class="muted">· world {Math.min(p.done + 1, p.total)} of {p.total}</span>{/if}
				</p>
			{:else if status.lastSync}
				<p class="sub muted">{status.lastResult} Last sync {ago(status.lastSync)}.</p>
			{:else}
				<p class="sub muted">{status.lastResult}</p>
			{/if}
		</div>
	</div>
	{#if syncing}
		<div class="bar" role="progressbar" aria-label="Sync progress" aria-valuemin="0" aria-valuemax="100" aria-valuenow={Math.round(pct)}>
			<span class="fill" style="width:{pct}%"></span>
			<span class="now" style="left:{pct}%; width:{p ? 100 / Math.max(p.total, 1) : 100}%"></span>
		</div>
	{/if}
</section>

{#each problems as pr}
	<section class="problem {pr.level}" role={pr.level === 'error' ? 'alert' : 'status'}>
		<h3>{pr.title}</h3>
		{#if pr.worlds?.length}<p class="small muted">Affects: {names(pr)}</p>{/if}
		{#if pr.action}<p>{pr.action}</p>{/if}
		{#if pr.fix && pr.fix !== '/' && fixLabel[pr.fix]}<a class="fixbtn" href={pr.fix}>{fixLabel[pr.fix]}</a>{/if}
		{#if pr.detail && pr.level === 'error'}
			<details>
				<summary>Details</summary>
				<code>{pr.detail}</code>
			</details>
		{/if}
	</section>
{/each}
{#each status.warnings ?? [] as w}
	{#if !problems.some((x) => x.kind === 'world-open')}<section class="problem info"><p>{w}</p></section>{/if}
{/each}

<style>
	section {
		background: var(--panel);
		border: 2px solid #000;
		box-shadow: inset 2px 2px 0 var(--panel-hi), inset -2px -2px 0 var(--panel-lo);
		padding: 10px 14px;
		margin: 14px 0 0;
		border-left: 6px solid var(--line);
	}
	.status.ok { border-left-color: var(--ok); }
	.status.busy, .status.info { border-left-color: var(--info); }
	.status.warn { border-left-color: var(--warn); }
	.status.error { border-left-color: var(--bad); }
	.row { display: flex; gap: 12px; align-items: center; }
	.gem { flex: none; width: 16px; height: 16px; background: var(--line); border: 2px solid #000; box-shadow: inset 2px 2px 0 #ffffff55; }
	.ok .gem { background: var(--ok); }
	.busy .gem, .info .gem { background: var(--info); }
	.warn .gem { background: var(--warn); }
	.error .gem { background: var(--bad); }
	.busy .gem { animation: blink 1s steps(2) infinite; }
	.text { min-width: 0; }
	h2 { margin: 0; font-size: 0.72rem; }
	.sub { margin: 4px 0 0; font-size: 0.92rem; }
	.bar {
		position: relative;
		height: 14px;
		margin-top: 10px;
		overflow: hidden;
		background: var(--panel-lo);
		border: 2px solid #000;
	}
	.fill, .now { position: absolute; top: 0; bottom: 0; }
	.fill { left: 0; background: var(--accent); transition: width 0.3s steps(6); }
	.now { background: repeating-linear-gradient(90deg, var(--accent) 0 8px, #3f8a2d 8px 16px); animation: blink 0.8s steps(2) infinite; }
	.problem h3 { font-size: 0.7rem; margin: 0 0 6px; }
	.problem p { margin: 4px 0; font-size: 0.92rem; }
	.problem.info { border-left-color: var(--info); }
	.problem.warn { border-left-color: var(--warn); }
	.problem.error { border-left-color: var(--bad); }
	.fixbtn {
		display: inline-block; margin-top: 6px; padding: 6px 12px; color: #fff; text-decoration: none;
		background: var(--btn); border: 2px solid #000;
		box-shadow: inset 2px 2px 0 #ffffff40, inset -2px -2px 0 #00000055;
	}
	.fixbtn:hover { background: #7d838b; }
	details { margin-top: 8px; font-size: 0.85rem; color: var(--muted); }
	summary { cursor: pointer; }
	code { display: block; margin-top: 6px; white-space: pre-wrap; }
	@keyframes blink { 50% { opacity: 0.55; } }
	@media (prefers-reduced-motion: reduce) { .gem, .now { animation: none; } .fill { transition: none; } }
</style>
