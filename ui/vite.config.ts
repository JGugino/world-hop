import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
	plugins: [sveltekit()],
	server: {
		// During `npm run dev`, proxy API calls to the running Go app.
		proxy: { '/api': 'http://127.0.0.1:47800' }
	}
});
