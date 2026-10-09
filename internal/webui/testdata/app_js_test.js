// Tests for static/js/app.js, run under node by TestAppJS (appjs_test.go).
//
// app.js is a browser script: one class and a DOMContentLoaded hook. It is
// loaded into a vm context with just enough of a DOM, and the class is used
// through its prototype so the constructor (which builds the page) never runs.
'use strict';

const assert = require('assert');
const fs = require('fs');
const vm = require('vm');

const source = fs.readFileSync(process.argv[2], 'utf8');

const tests = [];
const test = (name, fn) => tests.push({ name, fn });

// A tile element as updatePanelTile touches it.
function tileElement() {
    return {
        dataset: { waiting: '1' },
        textContent: '',
        innerHTML: '',
        querySelectorAll: () => [],
    };
}

// An app instance built without its constructor, from a document whose
// getElementById answers with the given element.
function loadWithElement(element) {
    const context = {
        console: { log() {}, warn() {}, error() {} },
        document: { addEventListener() {}, getElementById: () => element },
        window: {},
        WebSocket: { OPEN: 1 },
        setTimeout, clearTimeout,
    };
    vm.createContext(context);
    vm.runInContext(source, context, { filename: 'app.js' });
    const App = vm.runInContext('MonitoringApp', context);
    const app = Object.create(App.prototype);
    app.clearStatusNoData = () => {};
    app.markClippedReadings = () => {};
    app.escapeHtml = s => String(s);
    app.escapeAttr = s => String(s);
    return app;
}

function rowNames(html) {
    return [...html.matchAll(/<span class="panel-row-name">([^<]*)<\/span>/g)].map(m => m[1]);
}

test('multi-reading info metric on an indexed panel keeps the row identity in its names (#109)', () => {
    const element = tileElement();
    const app = loadWithElement(element);
    const panel = {
        type: 'text',
        title: 'Entity info',
        metric: 'snmp_entity_info',
        index_labels: ['ent_index'],
        series_labels: ['ent_name'],
    };
    const metrics = [
        { metric: { ent_index: '1', ent_name: 'Fan1', model: 'F-100', serial: 'A1' }, value: 1 },
        { metric: { ent_index: '2', ent_name: 'Fan2', model: 'F-100', serial: 'B2' }, value: 1 },
    ];
    app.updatePanelTile({ panel, elementId: 'x' }, metrics);

    const names = rowNames(element.innerHTML);
    assert.deepStrictEqual(names, [
        'Fan1 ent_name', 'Fan1 model', 'Fan1 serial',
        'Fan2 ent_name', 'Fan2 model', 'Fan2 serial',
    ]);
});

test('multi-reading info metric on an unindexed panel keeps the bare label', () => {
    const element = tileElement();
    const app = loadWithElement(element);
    const panel = { type: 'text', title: 'System', metric: 'snmp_system_info', index_labels: [] };
    app.updatePanelTile({ panel, elementId: 'x' }, [
        { metric: { sys_name: 'sw1', sys_descr: 'Switch' }, value: 1 },
    ]);
    assert.deepStrictEqual(rowNames(element.innerHTML), ['sys_descr', 'sys_name']);
});

// #110: a render that throws part way through subscribeToPanels must not
// leave a subscription recorded that was never sent.
function subscribeApp(sent) {
    const app = loadWithElement(null);
    app.activeSubscriptions = new Map();
    app.ws = { readyState: 1, send: m => sent.push(JSON.parse(m)) };
    app.WS_OPEN = 1;
    app.panelElementId = (d, m, p) => `${d}-${m}-${p.query}`;
    app.backfillPanel = () => Promise.resolve();
    return app;
}

test('a panel that throws while subscribing leaves no subscription recorded (#110)', () => {
    const sent = [];
    const app = subscribeApp(sent);
    app.initializeEmptyChart = (id) => {
        if (id.endsWith('bad')) throw new Error('chart will not initialise');
    };
    const panels = [
        { type: 'gauge', query: 'good', metric: 'm1' },
        { type: 'gauge', query: 'bad', metric: 'm2' },
    ];
    assert.throws(() => app.subscribeToPanels('dev', 'MIB', panels), /initialise/);
    assert.strictEqual(app.activeSubscriptions.has('dev-MIB'), false);
    assert.strictEqual(sent.length, 0);
});

test('a successful subscribeToPanels records and sends the subscription', () => {
    const sent = [];
    const app = subscribeApp(sent);
    app.initializeEmptyChart = () => {};
    app.subscribeToPanels('dev', 'MIB', [{ type: 'gauge', query: 'q1', metric: 'm1' }]);
    assert.strictEqual(app.activeSubscriptions.has('dev-MIB'), true);
    assert.deepStrictEqual(sent.map(m => m.queries), [['q1']]);
});

let failed = 0;
for (const { name, fn } of tests) {
    try {
        fn();
        console.log(`ok   ${name}`);
    } catch (error) {
        failed++;
        console.log(`FAIL ${name}\n${error.stack}`);
    }
}
process.exit(failed ? 1 : 0);
