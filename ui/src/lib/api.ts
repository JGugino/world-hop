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

export async function forceSync(): Promise<Status> {
	const r = await fetch('/api/sync', { method: 'POST' });
	if (!r.ok) throw new Error(`sync ${r.status}`);
	return r.json();
}
