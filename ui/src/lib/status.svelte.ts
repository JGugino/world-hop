import { getStatus, forceSync, type Status } from './api';

/** Shared sync status, polled by the layout and read by every page. */
class StatusStore {
	current = $state<Status | null>(null);
	error = $state('');
	/** Bumped whenever a sync finishes, so pages know to reload their data. */
	generation = $state(0);

	async refresh() {
		try {
			const prev = this.current;
			this.current = await getStatus();
			this.error = '';
			if (prev?.state === 'syncing' && this.current.state === 'idle') this.generation++;
		} catch (e) {
			this.error = e instanceof Error ? e.message : String(e);
		}
	}

	async sync() {
		try {
			this.current = { ...(this.current as Status), state: 'syncing' };
			this.current = await forceSync();
			this.error = '';
		} catch (e) {
			this.error = e instanceof Error ? e.message : String(e);
		}
		this.generation++;
	}
}

export const status = new StatusStore();
