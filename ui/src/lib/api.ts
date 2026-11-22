export type Status = {
	state: 'idle' | 'syncing';
	lastSync: string | null;
	lastResult: string;
	version: string;
};

export async function getStatus(): Promise<Status> {
	const r = await fetch('/api/status');
	if (!r.ok) throw new Error(`status ${r.status}`);
	return r.json();
}

// Tell the server this page is going away; it stops itself after a short grace
// period unless something else (e.g. a reload) talks to it first.
export function sayBye() {
	navigator.sendBeacon('/api/bye');
}

export async function forceSync(): Promise<Status> {
	const r = await fetch('/api/sync', { method: 'POST' });
	if (!r.ok) throw new Error(`sync ${r.status}`);
	return r.json();
}
