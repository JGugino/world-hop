<script lang="ts">
	import { onMount } from 'svelte';
	import {
		getWorlds,
		syncWorld,
		resolveConflict,
		getSnapshots,
		restoreSnapshot,
		type World,
		type WorldStatus,
		type Snapshot,
		type Strategy
	} from '$lib/api';
	import { status } from '$lib/status.svelte';

	let worlds = $state<World[] | null>(null);
	let error = $state('');
	let notice = $state('');
	let busy = $state<string | null>(null); // world id with an action running

	let openSnaps = $state<string | null>(null);
	let snaps = $state<Snapshot[]>([]);
	let confirmRestore = $state<string | null>(null);

	const labels: Record<WorldStatus, string> = {
		synced: 'Up to date',
		'local-ahead': 'Changes to upload',
		'remote-ahead': 'Newer version in the cloud',
		conflict: 'Conflict',
		'no-remote': 'Not uploaded yet',
		'no-local': 'Only in the cloud',
		unknown: 'Unknown'
	};
	const tone: Record<WorldStatus, string> = {
		synced: 'ok',
		'local-ahead': 'info',
		'remote-ahead': 'info',
		conflict: 'bad',
		'no-remote': 'info',
		'no-local': 'info',
		unknown: 'muted'
	};

	const choices: { s: Strategy; label: string; help: string }[] = [
		{ s: 'keep-local', label: 'Keep local', help: 'Use this computer’s version and upload it.' },
		{ s: 'keep-remote', label: 'Keep remote', help: 'Use the cloud version. A snapshot of this one is saved first.' },
		{ s: 'keep-newest', label: 'Keep newest', help: 'Use whichever was changed most recently.' },
		{ s: 'keep-both', label: 'Keep both', help: 'Copy this version to a new world folder, then use the cloud version.' }
	];

	async function load() {
		try {
			worlds = await getWorlds();
			error = '';
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}

	async function run(id: string, fn: () => Promise<unknown>) {
		busy = id;
		error = notice = '';
		try {
			await fn();
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
		busy = null;
		await load();
		status.refresh();
	}

	const sync = (w: World) => run(w.id, () => syncWorld(w.id));

	const resolve = (w: World, s: Strategy) =>
		run(w.id, async () => {
			const r = await resolveConflict(w.id, s);
			notice = `${w.name}: resolved with “${r.used}”.` + (r.copy ? ` Your version was kept at ${r.copy}.` : '');
		});

	async function toggleSnaps(w: World) {
		confirmRestore = null;
		if (openSnaps === w.id) {
			openSnaps = null;
			return;
		}
		try {
			snaps = await getSnapshots(w.id);
			openSnaps = w.id;
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}

	const restore = (w: World, s: Snapshot) =>
		run(w.id, async () => {
			await restoreSnapshot(w.id, s.id);
			confirmRestore = null;
			notice = `${w.name}: restored. The next sync uploads it as a new version.`;
			snaps = await getSnapshots(w.id);
		});

	function when(iso: string | null) {
		return iso ? new Date(iso).toLocaleString() : '';
	}

	onMount(() => {
		load();
		const t = setInterval(() => {
			if (!busy) load();
		}, 10000);
		return () => clearInterval(t);
	});

	// Reload right after a sync pass finishes.
	$effect(() => {
		status.generation;
		load();
	});
</script>

{#if error}<p class="msg bad">{error}</p>{/if}
{#if notice}<p class="msg ok">{notice}</p>{/if}

{#if worlds === null}
	<p class="muted">Loading…</p>
{:else if worlds.length === 0}
	<div class="card">
		<h2>No worlds yet</h2>
		<p class="muted">Add a saves folder or a single world on the Sources tab.</p>
		<a href="/sources">Go to Sources</a>
	</div>
{:else}
	{#each worlds as w (w.id)}
		<section class="card">
			<div class="head">
				<div>
					<h3>{w.name}</h3>
					<p class="muted small"><code>{w.path}</code></p>
				</div>
				<span class="badge {tone[w.status]}">{labels[w.status]}</span>
			</div>

			{#if w.open}
				<p class="note">Open in Minecraft. It is synced after you close it.</p>
			{/if}
			{#if w.error}<p class="note bad">{w.error}</p>{/if}

			{#if w.status === 'conflict'}
				<div class="conflict">
					<p>
						This world changed on this computer and somewhere else. Nothing has been
						overwritten. Choose what to keep:
					</p>
					<div class="row">
						{#each choices as c}
							<button
								class={c.s === 'keep-both' ? '' : 'secondary'}
								title={c.help}
								disabled={busy === w.id || w.open}
								onclick={() => resolve(w, c.s)}>{c.label}</button>
						{/each}
					</div>
					<ul class="muted small">
						{#each choices as c}<li><b>{c.label}:</b> {c.help}</li>{/each}
					</ul>
				</div>
			{/if}

			<div class="row">
				<button class="secondary" disabled={busy === w.id || w.open} onclick={() => sync(w)}>
					{busy === w.id ? 'Working…' : 'Sync this world'}
				</button>
				<button class="secondary" onclick={() => toggleSnaps(w)}>
					{openSnaps === w.id ? 'Hide snapshots' : 'Snapshots'}
				</button>
			</div>

			{#if openSnaps === w.id}
				<div class="snaps">
					{#if snaps.length === 0}
						<p class="muted small">
							No snapshots yet. One is saved automatically before any sync overwrites this world.
						</p>
					{:else}
						<ul>
							{#each snaps as s (s.id)}
								<li>
									<span>
										{when(s.createdAt) || s.id}
										{#if s.reason}<span class="muted small"> · {s.reason}</span>{/if}
									</span>
									{#if confirmRestore === s.id}
										<span class="row">
											<button class="danger" disabled={w.open || busy === w.id} onclick={() => restore(w, s)}>
												Replace current world
											</button>
											<button class="secondary" onclick={() => (confirmRestore = null)}>Cancel</button>
										</span>
									{:else}
										<button class="secondary" disabled={w.open} onclick={() => (confirmRestore = s.id)}>
											Restore
										</button>
									{/if}
								</li>
							{/each}
						</ul>
					{/if}
				</div>
			{/if}
		</section>
	{/each}
{/if}

<style>
	h3 { margin: 0; font-size: 1rem; }
	.head { display: flex; justify-content: space-between; gap: 12px; align-items: flex-start; }
	.row { display: flex; gap: 8px; flex-wrap: wrap; align-items: center; margin-top: 10px; }
	.badge { font-size: 0.8rem; padding: 2px 10px; border-radius: 999px; white-space: nowrap; border: 1px solid; }
	.badge.ok { color: var(--ok); }
	.badge.info { color: var(--accent); }
	.badge.bad { color: var(--bad); }
	.badge.muted { color: var(--muted); }
	.note { margin: 8px 0 0; font-size: 0.9rem; color: var(--warn); }
	.note.bad { color: var(--bad); }
	.conflict { border-left: 3px solid var(--bad); padding: 4px 12px; margin-top: 10px; }
	.conflict ul { padding-left: 18px; margin: 8px 0 0; }
	.snaps { margin-top: 10px; border-top: 1px solid var(--line); padding-top: 8px; }
	.snaps ul { list-style: none; margin: 0; padding: 0; }
	.snaps li { display: flex; justify-content: space-between; align-items: center; gap: 8px; padding: 6px 0; flex-wrap: wrap; }
	.msg { margin: 12px 0 0; padding: 8px 12px; border-radius: 6px; background: var(--panel); font-size: 0.9rem; }
	.msg.bad { border-left: 3px solid var(--bad); }
	.msg.ok { border-left: 3px solid var(--ok); }
	a { color: var(--accent); }
</style>
