import { createRoot } from 'react-dom/client';
import { DevelopmentConsole } from './console';
import './console.css';

const mode = new URL(import.meta.url).searchParams.get('mode');
if (mode !== 'fake' && mode !== 'real') throw new Error('开发控制台缺少模式');
const container = document.createElement('div');
container.id = 'kakatta-development-console';
document.body.prepend(container);
const root = createRoot(container);
root.render(<DevelopmentConsole mode={mode} />);
import.meta.hot?.dispose(() => {
  root.unmount();
  container.remove();
});
