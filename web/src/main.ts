import { mount } from 'svelte';
import './lib/ui/fonts.css';
import './lib/ui/tokens.css';
import './lib/ui/base.css';
import App from './App.svelte';

const target = document.getElementById('app');
if (!target) throw new Error('index.html has no #app element');

mount(App, { target });
