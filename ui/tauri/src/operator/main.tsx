import { createRoot } from 'react-dom/client';
import { OperatorApp } from './OperatorApp.tsx';
import { createMockAdapter } from './mock-adapter.ts';
import { createMemoryHistory } from './history.ts';
import '../../design/tokens.css';
import '../../design/components.css';
import '../app.css';
import './operator.css';

const mock = createMockAdapter();
let historyTime = Date.UTC(2026, 9, 28, 9);
const history = createMemoryHistory({ now: () => historyTime });
const advanceHistoryClock = () => { historyTime += 31 * 24 * 60 * 60 * 1000; history.prune(); };
const root = document.getElementById('root');
if (!root) throw new Error('MISSING_OPERATOR_ROOT');
// Only non-network IO: the visible copy action writes fixed sample metadata.
const copy = (text: string) => navigator.clipboard.writeText(text);
createRoot(root).render(<OperatorApp adapter={mock.adapter} emit={event => { mock.emit(event); }} copy={copy} history={history} advanceHistoryClock={advanceHistoryClock} />);
