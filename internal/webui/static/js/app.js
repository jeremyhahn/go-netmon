// go-netmon Real-time Monitoring Dashboard

// A chart created while its tab was hidden has to be given a provisional size,
// and ECharts then treats that size as fixed. Passing this to resize() hands
// measurement back to the DOM.
const AUTO_SIZE = { width: 'auto', height: 'auto' };

// MIB browser sizing. The library runs to ~170 modules and ~29k objects, so an
// expanded module paints its objects a page at a time, and "expand all" refuses
// to fan out past a handful of modules — one request each — without a filter.
const MIB_OBJECT_PAGE = 200;
const MIB_EXPAND_ALL_LIMIT = 20;
const MIB_EXPAND_ALL_CONCURRENCY = 4;

// List page size, and the length past which a list gains a filter box.
const LIST_PAGE = 50;
const LIST_CONTROLS_MIN = 8;
// How long a server-backed list waits before turning a typed search into a
// request, so a word costs one round trip rather than one per letter.
const LIST_SEARCH_DEBOUNCE = 250;

// The MIB browser pages smaller than the other lists. Each of its rows is an
// accordion that expands into a table of objects, so a screenful of them is far
// more than a screenful of ordinary rows.
const MIB_ACCORDION_PAGE = 25;

// Pager sizing: PAGER_WINDOW buttons around the current page; past PAGER_JUMP_MIN pages a go-to-page field.
const PAGER_WINDOW = 5;
const PAGER_JUMP_MIN = 8;

// Rows-per-page choices as multiples of a surface's page; remembered for the session.
const PAGER_SIZE_STEPS = [1, 2, 5];
const PAGER_SIZE_KEY = 'netmon-page-size';

// How long a widget waits for its first sample before it stops saying it is
// still loading. Charts and status tiles share the window so the same object
// reaches its terminal state at the same moment in both shapes.
const FIRST_SAMPLE_TIMEOUT = 30000;

// How often the bell re-reads what is firing. The server evaluates on its own
// interval, so asking faster only re-reads the same answer.
const ALARM_POLL_INTERVAL = 15000;

// Rows a tile shows before offering the rest.
const PANEL_ROWS_VISIBLE = 8;

// WS_SUPPORTS names the frame shapes this script can apply, sent with every subscribe.
const WS_SUPPORTS = ['all_steady'];

// PANEL_POINT_CAP bounds the points one series keeps. A panel of forty rows
// would otherwise hold forty times the history for the same span on screen.
const PANEL_POINT_CAP = 200;

// BACKFILL_RANGE is the window a chart requests when its panel registers.
const BACKFILL_RANGE = '6h';

// DEFAULT_SELECTED_ROWS is how many rows a table opens with charted, from the top of its order.
const DEFAULT_SELECTED_ROWS = 3;

// MAX_SELECTED_ROWS bounds what "select more" can do to a chart. A readability
// choice rather than a performance one: the charts draw from a stream that is
// already arriving, so the cost does not scale with the selection.
const MAX_SELECTED_ROWS = 8;

// Labels the bell does not repeat in an alarm's detail line: the device has its
// own line, and the rest are bookkeeping rather than anything to act on.
const ALARM_LABELS_HIDDEN = new Set(['__name__', 'device', 'source', 'instance', 'job']);

// UNATTRIBUTED_MODULE is what the server calls the metrics no MIB module
// claims, in its coverage table. The rail shows them under the same name, so a
// warning about the bucket and the tab holding it read as the same thing.
const UNATTRIBUTED_MODULE = '(no module)';

// UNREADABLE_MODULE holds everything when the catalog could not be read at all.
// Nothing has a module then, and filing it under "(no module)" would say no MIB
// claims these metrics when the truth is that nothing could be asked.
const UNREADABLE_MODULE = '(catalog unavailable)';

// The terminal wording, in one place, because a card and a tile describing the
// same silent object must not describe it differently. It says what is known —
// nothing arrived — and not why, which the UI cannot tell.
const NO_DATA_TITLE = 'No data reported';
const NO_DATA_DETAIL = 'The device returned no samples for this object.';

// Wording and colour for each /api/devices/status state.
const FLEET_STATES = {
    ok:             { label: 'Healthy',        tone: 'ok',      blurb: 'The last poll succeeded.' },
    failing:        { label: 'Failing',        tone: 'failing', blurb: 'A poll ran and did not succeed.' },
    unreachable:    { label: 'Unreachable',    tone: 'down',    blurb: 'Repeated polls went unanswered.' },
    never_polled:   { label: 'Never polled',   tone: 'pending', blurb: 'The poller has this device but has not completed a cycle for it yet.' },
    not_registered: { label: 'Not registered', tone: 'config',  blurb: 'The poller has no record of this device. That is a configuration fault, not a device fault.' },
    disabled:       { label: 'Disabled',       tone: 'unknown', blurb: 'Polling is switched off for this device. That is intended, not a fault.' },
    unknown:        { label: 'Unknown',        tone: 'unknown', blurb: 'The state could not be determined.' },
};

// The order the chips are laid out in. It is presentation only: the device list
// itself keeps the order the server sent, which is already deterministic.
const FLEET_STATE_ORDER = ['ok', 'failing', 'unreachable', 'never_polled', 'not_registered', 'unknown', 'disabled'];

// Severity, worst first. This is what the State column sorts by: triage order,
// not the alphabet. It is deliberately separate from FLEET_STATE_ORDER above,
// which lays out the chips and reads best healthy-first.
const FLEET_SEVERITY = ['unreachable', 'failing', 'not_registered', 'never_polled', 'unknown', 'ok', 'disabled'];

// A zero Go time.Time means never polled.
const ZERO_TIME_CEILING = Date.UTC(1971, 0, 1);

// Default seconds between re-reads of the surface on screen; the Settings interval overrides it.
const DEFAULT_REFRESH_SECONDS = 30;

// Bounds for the refresh interval, enforced on typed and restored values.
const MIN_REFRESH_SECONDS = 1;
const MAX_REFRESH_SECONDS = 60;

class MonitoringApp {
    constructor() {
        this.ws = null;
        this.charts = {};
        // Range reads that resolved before their card had an element to draw
        // on, keyed by that element's id and applied when it is built.
        this.pendingBackfill = new Map();

        // Row tables: what each row currently reads, and which rows the reader
        // has charted. Keyed by "<device>-<module>-<tableId>".
        this.tableRows = new Map();
        this.tableSelection = new Map();
        this.tableFilter = new Map();
        // Tables whose default selection was applied; an empty selection is the reader's choice.
        this.tableSeeded = new Set();
        this.paneTables = new Map();
        // Latest reading per subscribed query and its sequence: Map(query -> { seq, byKey: Map(key -> sample) }); deltas apply to this.
        this.lastReadings = new Map();

        // Queries this browser has asked the server to resend whole, so a run
        // of deltas it cannot apply does not become a run of requests.
        this.pendingResync = new Set();

        this.reconnectInterval = 3000;
        this.currentView = 'dashboard';
        this.currentDevice = null;
        this.currentProfile = null;
        this.devices = [];

        // This browser's refresh cadence in seconds and its timer; never affects polling.
        this.refreshIntervalSec = DEFAULT_REFRESH_SECONDS;
        this.refreshTimer = null;
        this.profiles = [];
        this.mibs = [];
        this.deviceDashboards = {}; // { deviceId: { mibs: [{name}], dashboards: Map(module -> {panels, warnings}) } }

        // MIB browser: the module list as last rendered, plus per-module details
        // fetched on first expand and kept so re-expanding costs nothing.
        this.mibsData = null;
        this.mibDetails = new Map();        // mibId -> detail payload
        this.mibDetailRequests = new Map(); // mibId -> in-flight promise
        this.mibAccordionBound = false;
        // Search boxes that live outside the list they drive, so their listener
        // is registered once rather than on every rebuild of that list.
        this.externalSearchBound = new Set();

        // Dashboard module rail: one set of listeners for the search box and
        // the arrow keys, installed the first time a device is rendered.
        this.mibTabNavBound = false;

        // Every paged list surface on the page, keyed by the id of the element
        // it renders into. See renderPagedList().
        this.pagedLists = new Map();

        // Fleet overview state: the last /api/devices/status page, filter chip, fetch time, totals and summary.
        this.fleet = {
            devices: [], total: 0, configured: 0, summary: {},
            stateFilter: '', fetchedAt: null, error: null
        };
        this.fleetRequest = null;
        this.fleetBound = false;

        // The element the description sheet was opened from, so closing it can
        // put focus back where it came from.
        this.descSheetTrigger = null;
        this.descSheetBound = false;

        // Per-object polling picker: the plan last fetched for one device+MIB
        // plus the assignment being edited against it. Null while closed.
        this.objectPicker = null;

        this.currentTimeRange = '6h';
        this.activeMibTab = null;
        this.tableMetadata = {}; // { mibId: { tables: [...] } }

        // Track active WebSocket subscriptions per dashboard
        // Key: "deviceId-mibName", Value: { metricQueries: Set(), statusQueries: Set(), objects: Map(query -> obj) }
        this.activeSubscriptions = new Map();

        // Module tabs rendered and subscribed, keyed "deviceId-mibName"; loaded lazily once.
        this.loadedMibTabs = new Set();

        // Track current selected device for re-subscription on reconnect
        this.currentDashboardDevice = null;

        // Which dashboard load is current; each step of a load gives up once this moves.
        this.dashboardLoad = 0;

        // Cards registered but not yet plotted; see initializeEmptyChart.
        this.pendingCharts = new Map();

        // Load state from localStorage
        this.loadState();

        this.init();
    }

    init() {
        this.setupNavigation();
        this.initializeCharts();
        this.connectWebSocket();
        this.loadDevices();
        // The MIB browser loads when it is first opened, not on every page load.
        this.setupEventListeners();
        this.bindRowTables();
        this.bindSeriesSearch();
        this.setupModals();

        // The landing surface is the fleet, not an empty frame waiting for a
        // choice. If a device was selected last session, restoreState() takes
        // over once the device list has loaded and hides this again.
        this.showFleetOverview();

        this.startDashboardRefresh();
        this.startAlarmPolling();
        this.bindAlarmBell();
        this.bindPanelExpanders();
    }

    // The alarm bell has its own slow tick, independent of the dashboard.
    startAlarmPolling() {
        const poll = () => {
            if (document.visibilityState === 'visible') this.refreshAlarms();
        };
        poll();
        setInterval(poll, ALARM_POLL_INTERVAL);
        document.addEventListener('visibilitychange', poll);
    }

    // refreshAlarms reads what is firing and redraws the bell.
    async refreshAlarms() {
        try {
            const response = await fetch('/api/alarms');
            if (!response.ok) return;
            this.renderAlarms(await response.json());
        } catch (error) {
            // An unreadable bell keeps its last badge and logs to the console.
            console.error('Could not read the alarms:', error);
        }
    }

    // renderAlarms draws the badge and the list behind it.
    renderAlarms(state) {
        const alarms = state.alarms || [];
        const badge = document.getElementById('alarmBadge');
        const button = document.getElementById('alarmBellBtn');
        const list = document.getElementById('alarmList');
        const watching = document.getElementById('alarmWatching');
        if (!badge || !button || !list) return;

        badge.textContent = alarms.length > 99 ? '99+' : String(alarms.length);
        badge.hidden = alarms.length === 0;

        // The bell's own colour is the worst thing in it, so the reader knows
        // whether to open it without opening it.
        const worst = alarms.length > 0 ? alarms[0].severity : '';
        button.classList.toggle('is-critical', worst === 'critical');
        button.classList.toggle('is-warning', worst === 'warning');
        button.classList.toggle('is-info', worst === 'info');
        button.setAttribute('title', alarms.length === 0
            ? 'Alarms — nothing firing'
            : `Alarms — ${alarms.length} firing`);

        if (watching) {
            // "Nothing is wrong", "nothing is being watched" and "nothing
            // could be asked" are three different answers and the bell looks
            // identical for all of them, so it says which.
            if (state.unevaluated > 0) {
                watching.textContent =
                    `${state.unevaluated} of ${state.configured} rules could not be evaluated`;
            } else {
                watching.textContent = state.configured === 0
                    ? 'No alarm rules are configured'
                    : `${state.configured} rule${state.configured === 1 ? '' : 's'} watching`;
            }
        }
        // A rule that could not be asked is not a rule that found nothing, and
        // the badge must not read as an all-clear while that is true.
        button.classList.toggle('is-degraded', state.unevaluated > 0);

        if (alarms.length === 0) {
            let empty;
            if (state.unevaluated > 0) {
                empty = 'None of the rules could be evaluated, so this is not an ' +
                    'all-clear. Check that Prometheus is answering.';
            } else if (state.configured === 0) {
                empty = 'Nothing is being watched. Add rules under <code>alarms:</code> ' +
                    'in the configuration file.';
            } else {
                empty = 'Nothing is firing.';
            }
            list.innerHTML = `<p class="alarm-empty">${empty}</p>`;
            return;
        }

        list.innerHTML = alarms.map(a => this.alarmRowHtml(a)).join('');
    }

    // alarmRowHtml is one firing alarm.
    alarmRowHtml(alarm) {
        // What the alarm is about beyond the device, such as the interface.
        const about = Object.entries(alarm.labels || {})
            .filter(([name]) => !ALARM_LABELS_HIDDEN.has(name))
            .sort(([a], [b]) => a.localeCompare(b))
            .map(([name, value]) => `${name}=${value}`)
            .join(' ');

        return `
            <article class="alarm-row is-${this.escapeAttr(alarm.severity || 'info')}">
                <div class="alarm-row-head">
                    <span class="alarm-severity">${this.escapeHtml(alarm.severity || '')}</span>
                    <span class="alarm-since" title="${this.escapeAttr(alarm.since || '')}">${
                        this.escapeHtml(this.sinceText(alarm.since))}</span>
                </div>
                <p class="alarm-summary">${this.escapeHtml(alarm.summary || alarm.rule)}</p>
                ${alarm.device ? `<p class="alarm-device">${this.escapeHtml(alarm.device)}</p>` : ''}
                ${about ? `<p class="alarm-about">${this.escapeHtml(about)}</p>` : ''}
                ${alarm.description ? `<p class="alarm-description">${
                    this.escapeHtml(alarm.description)}</p>` : ''}
            </article>
        `;
    }

    // sinceText says how long an alarm has been up, which is the thing a
    // reader wants before they want the timestamp.
    sinceText(since) {
        const at = Date.parse(since);
        if (isNaN(at)) return '';

        const seconds = Math.max(0, Math.round((Date.now() - at) / 1000));
        if (seconds < 60) return `${seconds}s`;
        if (seconds < 3600) return `${Math.floor(seconds / 60)}m`;
        if (seconds < 86400) return `${Math.floor(seconds / 3600)}h`;
        return `${Math.floor(seconds / 86400)}d`;
    }

    // bindPanelExpanders lets a tile show the rest of its rows, delegated from the tab content.
    bindPanelExpanders() {
        const content = document.getElementById('mibTabContent');
        if (!content || this.panelExpandersBound) return;
        this.panelExpandersBound = true;

        content.addEventListener('click', event => {
            const button = event.target.closest('[data-panel-more]');
            if (!button) return;

            const value = button.parentElement?.querySelector('.status-value')
                || button.previousElementSibling?.closest('.status-value')
                || button.closest('.panel')?.querySelector('.status-value');
            if (!value) return;

            value.dataset.expanded = value.dataset.expanded === '1' ? '0' : '1';

            // Redraw from what the browser already holds, so expanding does not
            // wait for the next tick.
            const target = this.panelTargetFor(value.id);
            if (target) {
                const held = this.lastReadings.get(target.panel.query);
                if (held) this.updatePanelTile(target, [...held.byKey.values()]);
            }
        });
    }

    // panelTargetFor finds the subscription entry that draws one element.
    panelTargetFor(elementId) {
        for (const [, subscription] of this.activeSubscriptions) {
            for (const [, target] of subscription.objects) {
                if (target.elementId === elementId) return target;
            }
        }
        return null;
    }

    // bindAlarmBell opens and closes the panel.
    bindAlarmBell() {
        const button = document.getElementById('alarmBellBtn');
        const panel = document.getElementById('alarmPanel');
        if (!button || !panel) return;

        const setOpen = open => {
            panel.hidden = !open;
            button.setAttribute('aria-expanded', open ? 'true' : 'false');
        };

        button.addEventListener('click', event => {
            event.stopPropagation();
            const opening = panel.hidden;
            setOpen(opening);
            // Opening it is a request for the current answer, not the one from
            // up to fifteen seconds ago.
            if (opening) this.refreshAlarms();
        });

        // Clicking away closes it, and so does Escape: a panel that can only
        // be closed by hitting the same small button again is a panel that
        // stays open.
        document.addEventListener('click', event => {
            if (!panel.hidden && !panel.contains(event.target)) setOpen(false);
        });
        document.addEventListener('keydown', event => {
            if (event.key === 'Escape' && !panel.hidden) {
                setOpen(false);
                button.focus();
            }
        });
    }

    // The single refresh tick drives the surface on screen; clampRefreshSeconds turns any input into a runnable cadence.
    clampRefreshSeconds(value) {
        // A blank value means the default, not the floor.
        if (value === null || value === undefined) return DEFAULT_REFRESH_SECONDS;
        if (typeof value === 'string' && value.trim() === '') return DEFAULT_REFRESH_SECONDS;

        const seconds = Math.round(Number(value));
        if (!Number.isFinite(seconds)) return DEFAULT_REFRESH_SECONDS;
        return Math.min(MAX_REFRESH_SECONDS, Math.max(MIN_REFRESH_SECONDS, seconds));
    }

    // applyRefreshInterval adopts a cadence, restarts the timer and returns the value applied.
    applyRefreshInterval(value) {
        this.refreshIntervalSec = this.clampRefreshSeconds(value);
        this.startDashboardRefresh();
        this.renderFleetHeader();
        this.renderDashboardCadence();
        return this.refreshIntervalSec;
    }

    startDashboardRefresh() {
        // Restarting is the normal case, not just startup: changing the
        // interval has to replace the timer rather than add a second one
        // running alongside it at the old rate.
        if (this.refreshTimer !== null) clearInterval(this.refreshTimer);
        this.refreshTimer = setInterval(async () => {
            if (!this.currentDashboardDevice) {
                // /api/devices/status is one request but it fans out to the
                // poller per device, so it is not asked for while the overview
                // is not the thing being looked at.
                if (this.fleetVisible() && document.visibilityState === 'visible') {
                    await this.refreshFleetStatus();
                }
                return;
            }
            try {
                // Refresh device status to update error banner
                await this.fetchAndDisplayDeviceStatus(this.currentDashboardDevice);

                // Has a MIB assigned to this device started producing data since the dashboard was drawn?
                const deviceName = this.deviceNameFor(this.currentDashboardDevice);
                if (!deviceName) return;

                const drawn = new Set(
                    (this.deviceDashboards[this.currentDashboardDevice]?.mibs || []).map(m => m.name));
                const response = await fetch(
                    `/api/mibs?filter=available&device=${encodeURIComponent(deviceName)}`);
                if (!response.ok) return;
                const data = await response.json();

                const appeared = (data.mibs || []).map(m => m.name).filter(name => !drawn.has(name));
                if (appeared.length > 0) {
                    console.log(`New MIB data for ${deviceName}: ${appeared.join(', ')}; reloading`);
                    await this.loadDeviceDashboards(this.currentDashboardDevice);
                }
            } catch (error) {
                console.error('Error polling for MIB data:', error);
            }
        }, this.refreshIntervalSec * 1000);
    }

    // renderDashboardCadence shows the device's poll interval (read-only) and this browser's refresh cadence.
    renderDashboardCadence() {
        const line = document.getElementById('dashboardCadence');
        if (!line) return;

        // The fleet header already states the re-read cadence for the overview,
        // so repeating it here would be two sentences saying one thing.
        if (!this.currentDashboardDevice) {
            line.textContent = '';
            return;
        }

        const wanted = String(this.currentDashboardDevice);
        const device = this.devices.find(d => String(d.id) === wanted);
        const polled = Number(device?.effective_poll_interval);

        const mine = `this browser re-reads every ${this.refreshIntervalSec}s`;
        if (!Number.isFinite(polled) || polled <= 0) {
            line.textContent = `Poll interval unknown for this device; ${mine}.`;
            return;
        }

        // Said plainly when the two disagree, because that gap is exactly the
        // confusion the issue was filed about: a reader watching a card refresh
        // every 5s assumes the device answered five seconds ago.
        const inherited = device?.poll_interval === null || device?.poll_interval === undefined;
        const source = inherited ? ' (the configured default)' : '';
        line.textContent =
            `Polled every ${polled}s${source} — new readings cannot arrive faster than that; ${mine}.`;
    }

    // deviceNameFor resolves a device id (string or number) to the name its metrics carry.
    deviceNameFor(deviceId) {
        if (deviceId === null || deviceId === undefined) return null;
        const wanted = String(deviceId);
        return this.devices.find(d => String(d.id) === wanted)?.name || null;
    }

    // ========================================
    // Navigation
    // ========================================

    setupNavigation() {
        document.getElementById('dashboardBtn').addEventListener('click', () => {
            this.switchView('dashboard');
            // Coming back to an unselected dashboard means coming back to the
            // fleet, and its numbers are as old as the view is.
            if (!this.currentDashboardDevice) this.showFleetOverview();
        });
        document.getElementById('devicesBtn').addEventListener('click', () => {
            this.switchView('devices');
            this.loadStoredDevices();
        });
        document.getElementById('profilesBtn').addEventListener('click', () => {
            this.switchView('profiles');
            this.loadProfiles();
        });
        document.getElementById('mibManagerBtn').addEventListener('click', () => {
            this.switchView('mibManager');
            this.loadStoredMIBs();
        });
        document.getElementById('mibBrowserBtn').addEventListener('click', () => {
            this.switchView('mibBrowser');
            this.loadMIBsAccordion();
        });

        // MIB Browser controls
        document.getElementById('expandAllMibs').addEventListener('click', () => this.expandAllMIBAccordion());
        document.getElementById('collapseAllMibs').addEventListener('click', () => this.collapseAllMIBAccordion());
        document.getElementById('settingsBtn').addEventListener('click', () => this.switchView('settings'));
    }

    switchView(view) {
        document.querySelectorAll('.view').forEach(v => v.classList.remove('active'));
        document.querySelectorAll('nav button').forEach(b => b.classList.remove('active'));

        const viewElement = document.getElementById(view + 'View');
        if (viewElement) {
            viewElement.classList.add('active');
        } else {
            console.error(`View element not found: ${view}View`);
        }

        const btnElement = document.getElementById(view + 'Btn');
        if (btnElement) {
            btnElement.classList.add('active');
        }
        // Note: Some views like 'deviceDetails' don't have navigation buttons, which is OK

        this.currentView = view;
    }

    // ========================================
    // Charts (Dashboard)
    // ========================================

    initializeCharts() {
        // Charts are now initialized dynamically when MIB tabs are loaded
        // This prevents errors from trying to initialize charts on non-existent elements

        // Resize charts on window resize
        window.addEventListener('resize', () => {
            Object.values(this.charts).forEach(chart => {
                if (chart && chart.resize) {
                    chart.resize(AUTO_SIZE);
                }
            });
        });
    }


    // ========================================
    // WebSocket
    // ========================================

    connectWebSocket() {
        const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
        const wsUrl = protocol + '//' + window.location.host + '/ws';

        this.ws = new WebSocket(wsUrl);

        this.ws.onopen = () => {
            console.log('WebSocket connected');
            this.updateStatus(true);

            // Subscribe only the device on screen.
            this.resubscribeActiveDashboards();
        };

        this.ws.onmessage = (event) => {
            const message = JSON.parse(event.data);
            this.handleMessage(message);
        };

        this.ws.onerror = (error) => {
            console.error('WebSocket error:', error);
            this.updateStatus(false);
        };

        this.ws.onclose = () => {
            console.log('WebSocket disconnected');
            this.updateStatus(false);
            setTimeout(() => this.connectWebSocket(), this.reconnectInterval);
        };
    }

    handleMessage(message) {
        switch (message.type) {
            case 'metric_snapshot':
                this.applySnapshot(message);
                return;

            case 'metric_delta':
                this.applyDelta(message);
                return;

            case 'metrics_unchanged':
                // Steady queries arrive by name; advance their charts to this tick (all_steady means every held query).
                if (message.all_steady) {
                    for (const [query, held] of this.lastReadings) {
                        this.updateDashboardCharts(query, [...held.byKey.values()],
                            message.timestamp);
                    }
                    return;
                }
                for (const query of message.queries || []) {
                    const held = this.lastReadings.get(query);
                    if (held) {
                        this.updateDashboardCharts(query, [...held.byKey.values()],
                            message.timestamp);
                    }
                }
                return;

            default:
                return;
        }
    }

    // applySnapshot replaces everything held for a query.
    applySnapshot(message) {
        // Ignore frames for queries no longer subscribed.
        if (!this.isSubscribed(message.query)) return;

        const byKey = new Map();
        for (const metric of message.metrics || []) {
            byKey.set(this.sampleKey(metric.metric), metric);
        }
        this.lastReadings.set(message.query, { seq: message.seq, byKey });
        this.pendingResync.delete(message.query);

        this.updateDashboardCharts(message.query, [...byKey.values()], message.timestamp);
    }

    // applyDelta applies a delta only to the sequence it was computed against; otherwise it discards it and requests a resync.
    applyDelta(message) {
        if (!this.isSubscribed(message.query)) return;

        const held = this.lastReadings.get(message.query);
        if (!held || held.seq !== message.base_seq) {
            this.requestResync(message.query);
            return;
        }

        const delta = message.delta || {};
        for (const metric of delta.changed || []) {
            held.byKey.set(this.sampleKey(metric.metric), metric);
        }
        for (const labels of delta.removed || []) {
            held.byKey.delete(this.sampleKey(labels));
        }
        held.seq = message.seq;

        this.updateDashboardCharts(message.query, [...held.byKey.values()], message.timestamp);
    }

    // isSubscribed reports whether any open tab still watches a query.
    isSubscribed(query) {
        for (const [, subscription] of this.activeSubscriptions) {
            if (subscription.objects.has(query)) return true;
        }
        return false;
    }

    // requestResync asks the server for a snapshot, once per query until it arrives.
    requestResync(query) {
        if (this.pendingResync.has(query)) return;
        this.pendingResync.add(query);

        console.warn(`Lost the place in ${query}; asking for the whole reading`);
        if (this.ws && this.ws.readyState === WebSocket.OPEN) {
            this.ws.send(JSON.stringify({ type: 'resync', queries: [query] }));
        }
    }

    // sampleKey identifies one series by its labels.
    sampleKey(labels) {
        return Object.keys(labels || {}).sort()
            .map(name => `${name.length}:${name}${String(labels[name]).length}:${labels[name]}`)
            .join('');
    }

    updateStatus(online) {
        const indicator = document.getElementById('statusIndicator');
        const text = document.getElementById('statusText');

        if (online) {
            indicator.className = 'status-dot online';
            text.textContent = 'Connected';
        } else {
            indicator.className = 'status-dot offline';
            text.textContent = 'Disconnected';
        }
    }

    // ========================================
    // Device Management
    // ========================================

    async loadStoredDevices() {
        try {
            const response = await fetch('/api/devices?stored=true');
            const data = await response.json();
            this.devices = data.devices || [];
            this.renderDevicesTable(this.devices);
        } catch (error) {
            console.error('Error loading devices:', error);
            this.showNotification('Failed to load devices', 'error');
        }
    }

    // ========================================
    // Pagination
    // ========================================

    // Pagination engine shared by every paged surface: previous, numbered pages, next and, for long sets, a page field.

    // pagerSlice turns a page request into the rows it selects. The page is
    // clamped rather than refused, so a filter that shortens the list lands the
    // reader on the last page that still exists instead of on an empty one.
    pagerSlice(total, page, size) {
        const pages = Math.max(1, Math.ceil(total / size));
        const asked = Number.isFinite(page) ? Math.trunc(page) : 0;
        const current = Math.min(Math.max(asked, 0), pages - 1);
        const from = Math.min(current * size, total);
        return { page: current, pages, size, from, to: Math.min(from + size, total) };
    }

    // pagerPages lists the numbered strip: first, last, PAGER_WINDOW around current, null for gaps; a one-page gap shows the page.
    pagerPages(current, pages) {
        if (pages <= PAGER_WINDOW + 2) {
            return Array.from({ length: pages }, (_, index) => index);
        }

        const half = Math.floor(PAGER_WINDOW / 2);
        let end = Math.min(current + half, pages - 2);
        let start = Math.max(end - PAGER_WINDOW + 1, 1);
        end = Math.min(start + PAGER_WINDOW - 1, pages - 2);

        const strip = [0];
        if (start > 1) strip.push(start === 2 ? 1 : null);
        for (let page = start; page <= end; page++) strip.push(page);
        if (end < pages - 2) strip.push(end === pages - 3 ? pages - 2 : null);
        strip.push(pages - 1);
        return strip;
    }

    // pagerNavHtml renders the pager as buttons, aria-current on the current page, end steps disabled.
    pagerNavHtml(navId, slice, label) {
        if (slice.pages <= 1) return '';

        // The chevrons are decoration, so they are hidden from assistive
        // technology and the word beside them is what both the eye and the
        // accessible name are built from.
        const prev = `
            <button type="button" class="pager-btn pager-step" data-pager-page="${slice.page - 1}"
                data-pager-rel="prev" aria-label="Previous page"
                ${slice.page === 0 ? 'disabled' : ''}><span aria-hidden="true">‹</span> Prev</button>`;
        const next = `
            <button type="button" class="pager-btn pager-step" data-pager-page="${slice.page + 1}"
                data-pager-rel="next" aria-label="Next page"
                ${slice.page >= slice.pages - 1 ? 'disabled' : ''}>Next <span aria-hidden="true">›</span></button>`;

        const strip = this.pagerPages(slice.page, slice.pages).map(page => {
            if (page === null) return '<span class="pager-gap" aria-hidden="true">…</span>';
            const current = page === slice.page;
            return `
                <button type="button" class="pager-btn pager-page${current ? ' is-current' : ''}"
                    data-pager-page="${page}" ${current ? 'aria-current="page"' : ''}
                    aria-label="Page ${(page + 1).toLocaleString()} of ${slice.pages.toLocaleString()}"
                    >${(page + 1).toLocaleString()}</button>`;
        }).join('');

        // Past PAGER_JUMP_MIN pages, a field to type a page number, prefilled with the current one.
        const jump = slice.pages >= PAGER_JUMP_MIN
            ? `<span class="pager-jump">
                   <label for="${navId}Jump">Go to page</label>
                   <input type="number" class="pager-jump-input" id="${navId}Jump"
                       min="1" max="${slice.pages}" step="1" value="${slice.page + 1}"
                       inputmode="numeric" autocomplete="off"
                       aria-label="Go to page, 1 to ${slice.pages.toLocaleString()}, ${this.escapeAttr(label)}">
                   <button type="button" class="secondary-btn pager-go" data-pager-jump="1">Go</button>
               </span>`
            : '';

        return `
            <span class="pager-strip">
                ${prev}
                ${strip}
                ${next}
            </span>
            ${jump}
        `;
    }

    // pagerStatus states position, and says what the numbers it quotes describe.
    // A filtered list counts what matched and names the set behind it, so
    // "1–50 of 120" is never mistaken for the whole library.
    pagerStatus(slice, matched, total, noun) {
        const range = matched === 0
            ? '0'
            : (slice.to - slice.from === 1
                ? slice.to.toLocaleString()
                : `${(slice.from + 1).toLocaleString()}–${slice.to.toLocaleString()}`);
        const scope = matched === total
            ? ''
            : ` matching, ${total.toLocaleString()} in total`;
        return `Showing ${range} of ${matched.toLocaleString()} ${noun}${scope}`;
    }

    // paintPager updates a surface's pager and live status in place.
    paintPager(navId, statusId, opts) {
        const nav = document.getElementById(navId);
        if (nav) {
            nav.innerHTML = this.pagerNavHtml(navId, opts.slice, opts.label);
            nav.hidden = opts.slice.pages <= 1;
        }
        const status = document.getElementById(statusId);
        if (status) {
            status.textContent = this.pagerStatus(opts.slice, opts.matched, opts.total, opts.noun);
        }
    }

    // bindPager wires one surface's pager. The container is an element that
    // outlives the nav, because the nav's contents are rebuilt on every page
    // change; `go` is handed the page the reader asked for and repaints.
    bindPager(container, go) {
        if (!container) return;

        container.addEventListener('click', (event) => {
            if (event.target.closest('[data-pager-jump]')) {
                this.runPagerJump(container, go);
                return;
            }
            const button = event.target.closest('button[data-pager-page]');
            if (!button || button.disabled) return;
            go(Number(button.dataset.pagerPage));
            this.restorePagerFocus(container, button.dataset.pagerRel);
        });

        container.addEventListener('keydown', (event) => {
            if (event.key !== 'Enter' || !event.target.closest('.pager-jump-input')) return;
            // The pager sits inside a form on some surfaces, so Enter has to be
            // claimed here or it submits the dialog instead of turning a page.
            event.preventDefault();
            this.runPagerJump(container, go);
        });
    }

    // runPagerJump reads the "go to page" field. A page outside the range is
    // clamped by pagerSlice and the field is then repainted with where the
    // reader actually landed, so the control never lies about the position.
    runPagerJump(container, go) {
        const field = container.querySelector('.pager-jump-input');
        if (!field) return;
        const asked = parseInt(field.value, 10);
        if (!Number.isFinite(asked)) return;
        go(asked - 1);
        const landed = container.querySelector('.pager-jump-input');
        if (landed) landed.focus({ preventScroll: true });
    }

    // Restore focus to the replaced control, or the current page when it became disabled.
    restorePagerFocus(container, rel) {
        const wanted = rel ? container.querySelector(`[data-pager-rel="${rel}"]:not([disabled])`) : null;
        const target = wanted || container.querySelector('.pager-page.is-current');
        if (target) target.focus({ preventScroll: true });
    }

    // Rows-per-page is remembered for the session and keyed by surface, so
    // choosing 1,000 objects while reading one module does not silently reshape
    // the device list in another tab of the app.
    pagerSizes() {
        try {
            const raw = sessionStorage.getItem(PAGER_SIZE_KEY);
            const parsed = raw ? JSON.parse(raw) : {};
            return parsed && typeof parsed === 'object' ? parsed : {};
        } catch (error) {
            return {};
        }
    }

    pagerSizeOptions(base) {
        return PAGER_SIZE_STEPS.map(step => base * step);
    }

    pagerSize(key, base) {
        const stored = this.pagerSizes()[key];
        return this.pagerSizeOptions(base).includes(stored) ? stored : base;
    }

    setPagerSize(key, size) {
        try {
            const sizes = this.pagerSizes();
            sizes[key] = size;
            sessionStorage.setItem(PAGER_SIZE_KEY, JSON.stringify(sizes));
        } catch (error) {
            // A session without storage still pages; it just forgets the choice.
        }
    }

    // The page-size control sits beside the search box, outside the repainted footer.
    pagerSizeHtml(id, base, size, noun) {
        const options = this.pagerSizeOptions(base)
            .map(option => `<option value="${option}" ${option === size ? 'selected' : ''}>${option.toLocaleString()}</option>`)
            .join('');
        return `
            <label class="pager-size">
                <span>Rows</span>
                <select class="pager-size-select" id="${id}"
                    aria-label="Rows per page, ${this.escapeAttr(noun)}">${options}</select>
            </label>
        `;
    }

    // ========================================
    // Sorting
    // ========================================

    // Column sorting for paged surfaces, applied after filter and search and before paging.

    // sortCompare orders two values of a column; missing values always sink.
    sortCompare(a, b, dir) {
        const aMissing = a === null || a === undefined || a === '';
        const bMissing = b === null || b === undefined || b === '';
        if (aMissing || bMissing) {
            if (aMissing && bMissing) return 0;
            return aMissing ? 1 : -1;
        }

        let order;
        if (typeof a === 'number' && typeof b === 'number') {
            order = a - b;
        } else {
            // numeric:true so eth10 sorts after eth9 rather than after eth1.
            order = String(a).localeCompare(String(b), undefined, {
                numeric: true, sensitivity: 'base'
            });
        }
        return dir === 'desc' ? -order : order;
    }

    // sortItems returns a stable sorted copy.
    sortItems(items, state) {
        const column = (state.sortColumns || []).find(c => c.key === state.sortKey);
        if (!column || !column.get) return items;

        return items
            .map((item, index) => ({ item, index }))
            .sort((a, b) => {
                const order = this.sortCompare(column.get(a.item), column.get(b.item), state.sortDir);
                return order !== 0 ? order : a.index - b.index;
            })
            .map(entry => entry.item);
    }

    // sortHeaderHtml renders a header cell as a button, with aria-sort on the th.
    sortHeaderHtml(mountId, column, state) {
        const active = state.sortKey === column.key;
        const dir = active ? state.sortDir : null;
        const aria = active ? (dir === 'asc' ? 'ascending' : 'descending') : 'none';
        // The arrow is decoration; the accessible name says the same thing in
        // words, including what the next click will do.
        const arrow = active ? (dir === 'asc' ? '▲' : '▼') : '';
        const next = active && dir === 'asc' ? 'descending' : 'ascending';
        const className = column.className ? ` class="${this.escapeAttr(column.className)}"` : '';

        return `<th${className} aria-sort="${aria}" data-sort-cell="${this.escapeAttr(column.key)}">
            <button type="button" class="sort-header${active ? ' is-sorted' : ''}"
                    data-sort-key="${this.escapeAttr(column.key)}"
                    aria-label="${this.escapeAttr(column.label)}, sort ${next}">
                <span>${this.escapeHtml(column.label)}</span>
                <span class="sort-arrow" aria-hidden="true">${arrow}</span>
            </button>
        </th>`;
    }

    // sortHeadersHtml renders a whole header row, for a wrap() to drop into its
    // thead. Columns without a get() are rendered as plain headers, so a column
    // holding a control or a free-text reason is not offered as sortable.
    sortHeadersHtml(mountId) {
        const state = this.pagedLists.get(mountId);
        if (!state) return '';

        return (state.sortColumns || []).map(column => {
            if (!column.get) {
                const className = column.className ? ` class="${this.escapeAttr(column.className)}"` : '';
                return `<th${className}>${this.escapeHtml(column.label)}</th>`;
            }
            return this.sortHeaderHtml(mountId, column, state);
        }).join('');
    }

    // sortControlsHtml offers the sort columns as a button row for non-table surfaces.
    sortControlsHtml(mountId) {
        const state = this.pagedLists.get(mountId);
        if (!state || !state.sortColumns) return '';

        const buttons = state.sortColumns.filter(column => column.get).map(column => {
            const active = state.sortKey === column.key;
            const dir = active ? state.sortDir : null;
            const arrow = active ? (dir === 'asc' ? '▲' : '▼') : '';
            const next = active && dir === 'asc' ? 'descending' : 'ascending';
            return `
                <button type="button" class="sort-chip${active ? ' is-sorted' : ''}"
                        data-sort-key="${this.escapeAttr(column.key)}"
                        ${active ? 'aria-pressed="true"' : 'aria-pressed="false"'}
                        aria-label="Sort by ${this.escapeAttr(column.label)}, ${next}">
                    ${this.escapeHtml(column.label)}
                    <span class="sort-arrow" aria-hidden="true">${arrow}</span>
                </button>`;
        }).join('');

        return `<span class="sort-chips-label">Sort by</span>${buttons}`;
    }

    // sortPagedList applies a header click: same column reverses, a new one takes its natural direction.
    sortPagedList(mountId, key) {
        const state = this.pagedLists.get(mountId);
        if (!state) return;

        const column = (state.sortColumns || []).find(c => c.key === key);
        if (!column || !column.get) return;

        if (state.sortKey === key) {
            state.sortDir = state.sortDir === 'asc' ? 'desc' : 'asc';
        } else {
            state.sortKey = key;
            state.sortDir = column.firstDir || 'asc';
        }
        // A reorder is a different set in a different order: read from its
        // start, exactly as a new search term is.
        state.page = 0;
        this.repagePagedList(mountId);
        this.restoreSortFocus(mountId, key);
    }

    // The header that was clicked is replaced by the repaint, so focus moves to
    // its replacement rather than being dropped at the top of the document.
    restoreSortFocus(mountId, key) {
        const mount = document.getElementById(mountId);
        if (!mount) return;
        const button = mount.querySelector(`[data-sort-key="${CSS.escape(key)}"]`);
        if (button) button.focus({ preventScroll: true });
    }

    // ========================================
    // Paged list surfaces
    // ========================================

    // Paged list with filter box and pager; options: items, matches, filter, renderRows, wrap, noun, placeholder, emptyHtml, pageSize, sizeKey, pagerLabel.
    renderPagedList(mountId, options) {
        const mount = document.getElementById(mountId);
        if (!mount) return;

        const base = options.pageSize || LIST_PAGE;
        const sizeKey = options.sizeKey || mountId;
        const items = options.items || [];
        const label = options.pagerLabel || options.noun || 'rows';
        const state = {
            items,
            matches: options.matches,
            filter: options.filter || null,
            renderRows: options.renderRows,
            wrap: options.wrap || (rows => `<div class="list-items">${rows}</div>`),
            noun: options.noun || 'rows',
            label,
            base,
            sizeKey,
            size: this.pagerSize(sizeKey, base),
            search: '',
            page: 0,
            // Sorting is opt-in. A surface that declares no columns keeps the
            // order the server sent, which is already deterministic.
            sortColumns: options.sortColumns || null,
            sortKey: options.sortKey || null,
            sortDir: options.sortDir || 'asc',
            // Rows whose behaviour is bound rather than delegated need to be
            // rebound after each repaint, because the repaint replaced them.
            afterPaint: options.afterPaint || null,
            // Both or neither: a repaint edits rows in place only when the list
            // can say which row is which and how to bring one up to date.
            rowKey: options.rowKey || null,
            patchRow: options.patchRow || null,
            // A list whose rows live on the server declares a source and holds one page.
            source: options.source || null,
            // What the server says matches, which the pager needs and the page
            // in hand cannot tell it.
            total: options.total || 0,
            // And how many there are before any narrowing, so the pager can say
            // "of 200 in total" while a filter is showing twelve of them.
            grandTotal: options.grandTotal || 0
        };
        this.pagedLists.set(mountId, state);

        if (items.length === 0) {
            mount.innerHTML = options.emptyHtml || '<p class="empty-state">Nothing here yet</p>';
            return;
        }

        // LIST_CONTROLS_MIN keeps the search box off short lists; searchInput binds to an existing box instead.
        const wantsControls = !options.searchInput
            && (options.alwaysControls || items.length >= LIST_CONTROLS_MIN);
        const search = wantsControls
            ? `<input type="search" class="paged-list-search" id="${mountId}Search"
                      placeholder="${this.escapeAttr(options.placeholder || 'Search…')}"
                      aria-label="${this.escapeAttr(options.placeholder || 'Search')}"
                      aria-controls="${mountId}Body" autocomplete="off">`
            : '';
        // Rows-per-page is only a question once there is more than one page to
        // spend it on; below that it is chrome offering a choice that changes
        // nothing.
        const size = items.length > base
            ? this.pagerSizeHtml(`${mountId}Size`, base, state.size, state.noun)
            : '';
        const controls = search || size
            ? `<div class="paged-list-controls">${search}${size}</div>`
            : '';

        mount.innerHTML = `
            <div class="paged-list">
                ${controls}
                <div class="paged-list-body" id="${mountId}Body"></div>
                <div class="paged-list-footer" id="${mountId}Footer">
                    <nav class="pager-nav" id="${mountId}Pager"
                         aria-label="${this.escapeAttr(label)} pagination" hidden></nav>
                    <p class="pager-status" id="${mountId}Status" role="status" aria-live="polite"></p>
                </div>
            </div>
        `;

        // Build the shell once and repaint only body, pager and status.
        if (state.sortColumns) {
            mount.addEventListener('click', (event) => {
                const header = event.target.closest('[data-sort-key]');
                if (header) this.sortPagedList(mountId, header.dataset.sortKey);
            });
        }

        const searchBox = options.searchInput
            ? document.getElementById(options.searchInput)
            : mount.querySelector('.paged-list-search');
        if (searchBox) {
            // Honour whatever is already in the search box on first render.
            state.search = searchBox.value || '';

            // Register an external box's listener once.
            const bindOnce = options.searchInput
                && this.externalSearchBound.has(options.searchInput);
            if (options.searchInput) {
                this.externalSearchBound.add(options.searchInput);
            }
            if (!bindOnce) {
                searchBox.addEventListener('input', () => {
                    // The live state, not the one captured when this listener
                    // was made: the list is rebuilt whenever its data changes.
                    const live = this.pagedLists.get(mountId);
                    if (!live) return;
                    live.search = searchBox.value;
                    // A narrower set is a different set: read from its start.
                    live.page = 0;
                    this.repagePagedList(mountId);
                });
            }
        }

        const sizeSelect = mount.querySelector('.pager-size-select');
        if (sizeSelect) {
            sizeSelect.addEventListener('change', () => {
                const next = parseInt(sizeSelect.value, 10);
                if (!Number.isFinite(next) || next <= 0) return;
                // Re-paging keeps the first row on screen where it was, so
                // resizing the page moves the boundaries and not the reader.
                state.page = Math.floor((state.page * state.size) / next);
                state.size = next;
                this.setPagerSize(state.sizeKey, next);
                this.repagePagedList(mountId);
            });
        }

        this.bindPager(mount.querySelector('.paged-list-footer'), (page) => {
            state.page = page;
            this.repagePagedList(mountId);
        });

        this.paintPagedList(mountId);
    }

    // repagePagedList redraws after a move, search or sort; server-backed lists coalesce requests, latest wins.
    repagePagedList(mountId) {
        const state = this.pagedLists.get(mountId);
        if (!state) return;

        if (!state.source) {
            this.paintPagedList(mountId);
            return;
        }

        const generation = (state.generation || 0) + 1;
        state.generation = generation;

        clearTimeout(state.pendingFetch);
        state.pendingFetch = setTimeout(async () => {
            try {
                const answer = await state.source({
                    search: state.search.trim(),
                    page: state.page,
                    size: state.size,
                    sortKey: state.sortKey,
                    sortDir: state.sortDir
                });
                // A later request has already been made; this answer describes
                // a view the reader has moved on from.
                if (state.generation !== generation) return;
                if (answer) {
                    state.items = answer.items || [];
                    state.total = answer.total || 0;
                    state.grandTotal = answer.grandTotal || answer.total || 0;
                }
            } catch (error) {
                console.error(`Loading a page of ${mountId} failed:`, error);
            } finally {
                if (state.generation === generation) this.paintPagedList(mountId);
            }
        }, state.items.length ? LIST_SEARCH_DEBOUNCE : 0);
    }

    paintPagedList(mountId) {
        const state = this.pagedLists.get(mountId);
        if (!state) return;

        const body = document.getElementById(`${mountId}Body`);
        const footer = document.getElementById(`${mountId}Footer`);
        if (!body || !footer) return;

        // Filter, then search; server-fetched rows are already the answer and are not filtered again.
        const server = Boolean(state.source);
        let ordered;
        let slice;
        let page;
        if (server) {
            ordered = state.items;
            slice = this.pagerSlice(state.total, state.page, state.size);
            state.page = slice.page;
            page = state.items;
        } else {
            // The filter runs first and the search narrows what it left, so the
            // two compose instead of replacing one another, and the counts below
            // describe the set the reader can actually see.
            const term = state.search.trim().toLowerCase();
            const scoped = state.filter ? state.items.filter(state.filter) : state.items;
            const matched = term ? scoped.filter(item => state.matches(item, term)) : scoped;

            ordered = state.sortKey ? this.sortItems(matched, state) : matched;

            slice = this.pagerSlice(ordered.length, state.page, state.size);
            state.page = slice.page;

            page = ordered.slice(slice.from, slice.to);
        }

        // With rowKey and patchRow, a refresh patches changed cells instead of replacing the table.
        if (!this.patchPagedRows(state, body, page)) {
            body.innerHTML = page.length === 0
                ? `<p class="empty-state">No ${this.escapeHtml(state.noun)} match “${this.escapeHtml(state.search.trim())}”</p>`
                : state.wrap(state.renderRows(page), mountId);
        }

        // A list that fits on one page and is not being filtered has no
        // position to report and nowhere to step to, so it gets no footer.
        if (state.afterPaint) state.afterPaint();

        const matched = server ? state.total : ordered.length;
        const total = server ? (state.grandTotal || state.total) : state.items.length;
        footer.hidden = slice.pages <= 1 && matched === total;
        this.paintPager(`${mountId}Pager`, `${mountId}Status`, {
            slice,
            matched,
            total,
            noun: state.noun,
            label: state.label
        });
    }

    // patchPagedRows updates a rendered page in place when the same rows are on screen, reporting whether it could.
    patchPagedRows(state, body, page) {
        if (!state.rowKey || !state.patchRow || page.length === 0) return false;

        const rows = body.querySelectorAll('[data-row-key]');
        if (rows.length !== page.length) return false;

        for (let i = 0; i < page.length; i++) {
            if (rows[i].dataset.rowKey !== String(state.rowKey(page[i]))) return false;
        }

        for (let i = 0; i < page.length; i++) {
            state.patchRow(rows[i], page[i]);
        }
        return true;
    }

    // setCellHtml writes into a cell only when the markup differs, so a value
    // that has not moved does not disturb the node holding it.
    setCellHtml(row, selector, html) {
        const cell = row.querySelector(selector);
        if (cell && cell.innerHTML !== html) cell.innerHTML = html;
    }

    // ========================================
    // Description sheet
    // ========================================

    // descriptionTriggerHtml renders a clipped MIB description as a control that opens the full text in the description sheet.
    descriptionTriggerHtml(description, detail = {}) {
        const text = String(description == null ? '' : description).trim();
        if (text === '') return '';

        const meta = (detail.meta || [])
            .filter(([, value]) => value !== undefined && value !== null && value !== '');

        // A compact trigger shows an info glyph and "Description"; the text travels in data-desc-text.
        const content = detail.compact
            ? '<span class="desc-trigger-icon" aria-hidden="true">ⓘ</span>' +
              '<span class="desc-trigger-label">Description</span>'
            : this.escapeHtml(text);
        const label = detail.compact ? ' aria-label="Read the description"' : '';

        return `<button type="button" class="desc-trigger${detail.className ? ' ' + detail.className : ''}"${label}
                    aria-haspopup="dialog" title="Read the full description"
                    data-desc-title="${this.escapeAttr(detail.title || '')}"
                    data-desc-eyebrow="${this.escapeAttr(detail.eyebrow || '')}"
                    data-desc-meta="${this.escapeAttr(JSON.stringify(meta))}"
                    data-desc-text="${this.escapeAttr(text)}"
                >${content}</button>`;
    }

    // A table cell or list line whose description would otherwise run away with
    // the column: clipped to one line, and readable in full from the sheet.
    descriptionCellHtml(description, name, eyebrow) {
        return this.descriptionTriggerHtml(description, {
            title: name,
            eyebrow,
            className: 'desc-clamp'
        });
    }

    // One delegated listener for every trigger on the page, however the row it
    // sits in was rendered.
    bindDescriptionSheet() {
        if (this.descSheetBound) return;

        document.addEventListener('click', (event) => {
            const trigger = event.target.closest('.desc-trigger');
            if (trigger) {
                event.preventDefault();
                this.openDescriptionSheet(trigger);
                return;
            }
            if (event.target.closest('#descSheetClose')) this.closeDescriptionSheet();
        });

        document.addEventListener('keydown', (event) => {
            if (event.key !== 'Escape') return;
            const sheet = document.getElementById('descSheet');
            if (sheet && !sheet.hidden) this.closeDescriptionSheet();
        });

        this.descSheetBound = true;
    }

    openDescriptionSheet(trigger) {
        const sheet = document.getElementById('descSheet');
        if (!sheet) return;

        let meta = [];
        try {
            meta = JSON.parse(trigger.dataset.descMeta || '[]');
        } catch (error) {
            console.error('Malformed description metadata:', error);
        }

        document.getElementById('descSheetEyebrow').textContent = trigger.dataset.descEyebrow || '';
        document.getElementById('descSheetTitle').textContent = trigger.dataset.descTitle || 'Description';
        document.getElementById('descSheetMeta').innerHTML = meta
            .map(([label, value]) => `
                <div>
                    <dt>${this.escapeHtml(label)}</dt>
                    <dd>${this.escapeHtml(value)}</dd>
                </div>
            `).join('');
        // Reflow the DESCRIPTION's line wrapping, keeping blank-line paragraphs.
        document.getElementById('descSheetBody').innerHTML = String(trigger.dataset.descText || '')
            .split(/\n\s*\n/)
            .map(paragraph => `<p>${this.escapeHtml(paragraph.replace(/\s*\n\s*/g, ' ').trim())}</p>`)
            .join('');

        this.descSheetTrigger = trigger;
        sheet.hidden = false;
        // Non-modal: the page behind stays live, so a reader can keep working
        // down a list of objects and watch the sheet follow.
        sheet.focus();
    }

    closeDescriptionSheet() {
        const sheet = document.getElementById('descSheet');
        if (!sheet || sheet.hidden) return;

        sheet.hidden = true;
        const trigger = this.descSheetTrigger;
        this.descSheetTrigger = null;
        if (trigger && document.body.contains(trigger)) trigger.focus();
    }

    // Fleet overview: every configured device and its state in a table, filtered by one chip per state.

    // showFleetOverview makes the overview the visible surface and refreshes it.
    // Called on load, when the device selector is cleared, and when the
    // Dashboard tab is re-entered with nothing selected.
    showFleetOverview() {
        // Showing the fleet abandons any dashboard load in flight.
        this.dashboardLoad++;

        const content = document.getElementById('dashboardContent');
        if (content) content.style.display = '';
        const tabs = document.getElementById('mibTabsContainer');
        if (tabs) tabs.style.display = 'none';

        this.renderFleetOverview();
        this.refreshFleetStatus();
    }

    // Whether the overview is the thing on screen. The refresh tick asks before
    // spending a request on it.
    fleetVisible() {
        return this.currentView === 'dashboard' && !this.currentDashboardDevice;
    }

    // One request for the whole fleet, and only ever one in flight. The server's
    // ordering is deterministic and is kept exactly as it arrives.
    async refreshFleetStatus() {
        if (this.fleetRequest) return this.fleetRequest;

        this.fleetRequest = (async () => {
            try {
                const data = await this.fetchFleetPage();
                this.fleet.devices = Array.isArray(data.devices) ? data.devices : [];
                this.fleet.total = data.total || 0;
                this.fleet.configured = data.configured || 0;
                this.fleet.summary = data.summary || {};
                this.fleet.error = null;
                // Only a read that worked moves the clock, so a failed refresh
                // cannot make stale rows look freshly taken.
                this.fleet.fetchedAt = Date.now();
            } catch (error) {
                // The last good payload stays on screen; the header says how old
                // it is and what went wrong, which beats blanking the fleet.
                this.fleet.error = error.message || String(error);
                console.error('Error loading fleet status:', error);
            } finally {
                this.fleetRequest = null;
                if (this.fleetVisible()) this.renderFleetOverview();
            }
        })();

        return this.fleetRequest;
    }

    // fetchFleetPage requests the visible page of the fleet plus whole-fleet counts.
    async fetchFleetPage() {
        const list = this.pagedLists.get('fleetDeviceList');
        const size = list ? list.size : LIST_PAGE;
        const page = list ? list.page : 0;
        const search = list ? list.search.trim() : '';

        const params = new URLSearchParams({ limit: String(size), offset: String(page * size) });
        if (search) params.set('search', search);
        if (this.fleet.stateFilter) params.set('state', this.fleet.stateFilter);

        const response = await fetch(`/api/devices/status?${params}`);
        if (!response.ok) throw new Error(`the status endpoint answered ${response.status}`);
        return response.json();
    }

    renderFleetOverview() {
        const content = document.getElementById('dashboardContent');
        if (!content) return;

        // loadDeviceDashboards writes its own empty states into this container,
        // so the shell is rebuilt whenever it has been taken over.
        if (!document.getElementById('fleetOverview')) {
            content.innerHTML = `
                <section class="fleet-overview" id="fleetOverview" aria-labelledby="fleetHeading">
                    <div class="fleet-head">
                        <h2 id="fleetHeading">Fleet overview</h2>
                        <p class="fleet-updated" id="fleetUpdated"></p>
                    </div>
                    <div class="fleet-summary" id="fleetSummary" role="group"
                         aria-label="Devices by poller state"></div>
                    <div class="table-container fleet-list" id="fleetDeviceList"></div>
                </section>
            `;
            this.pagedLists.delete('fleetDeviceList');
        }

        this.bindFleetOverview();
        this.renderFleetHeader();
        this.renderFleetSummary();
        this.renderFleetList();
    }

    renderFleetHeader() {
        const header = document.getElementById('fleetUpdated');
        if (!header) return;

        const count = this.fleet.configured;
        const noun = `${count.toLocaleString()} configured device${count === 1 ? '' : 's'}`;
        // Every relative time on this surface is frozen between refreshes, so
        // the header says which read they all belong to and how often it moves.
        const age = this.relativeTime(this.fleet.fetchedAt);

        header.classList.toggle('is-error', Boolean(this.fleet.error));
        if (this.fleet.error) {
            const carried = this.fleet.fetchedAt === null
                ? 'No state has been read yet.'
                : `Showing the last state for ${noun}, read ${age}.`;
            header.textContent = `Could not read device status — ${this.fleet.error}. `
                + `${carried} The next refresh will try again.`;
            return;
        }
        header.textContent = this.fleet.fetchedAt === null
            ? 'Reading device status…'
            : `Poller state for ${noun} · read ${age}, `
                + `refreshing every ${this.refreshIntervalSec}s`;
    }

    // One chip per state; Healthy always shows, the rest only when non-zero.
    renderFleetSummary() {
        const summary = document.getElementById('fleetSummary');
        if (!summary) return;

        // A strip counting nothing is chrome. With no fleet on screen — because
        // none is configured, or because the read failed — the list below says
        // which of those two it is, and says it once.
        if (this.fleet.configured === 0) {
            summary.innerHTML = '';
            return;
        }

        // Counted across every configured device by the server. Counting the
        // rows in hand would make each chip read as "how many of this page",
        // which is not what a fleet tally means.
        const counts = new Map();
        for (const [state, count] of Object.entries(this.fleet.summary || {})) {
            counts.set(this.fleetStateMeta(state).key, count);
        }

        // A chip whose devices have all left the fleet cannot stay selected, or
        // the table below it would be filtered to nothing by an invisible rule.
        if (this.fleet.stateFilter && !counts.get(this.fleet.stateFilter)) {
            this.fleet.stateFilter = '';
        }

        const keys = FLEET_STATE_ORDER.filter(key => key === 'ok' || counts.get(key));
        for (const key of counts.keys()) {
            if (!keys.includes(key)) keys.push(key);
        }

        const total = this.fleet.configured;
        summary.innerHTML = [
            this.renderFleetChip('', 'All devices', total, 'total'),
            ...keys.map(key => {
                const meta = this.fleetStateMeta(key);
                return this.renderFleetChip(key, meta.label, counts.get(key) || 0, meta.tone);
            }),
        ].join('');
    }

    renderFleetChip(stateKey, label, count, tone) {
        const active = this.fleet.stateFilter === stateKey;
        const dot = stateKey ? '<span class="fleet-dot"></span>' : '';
        return `
            <button type="button" class="fleet-chip fleet-tone--${this.escapeAttr(tone)}"
                    data-fleet-state="${this.escapeAttr(stateKey)}" aria-pressed="${active}">
                <span class="fleet-chip-count">${count.toLocaleString()}</span>
                <span class="fleet-chip-label">${dot}${this.escapeHtml(label)}</span>
            </button>
        `;
    }

    // The list itself is renderPagedList, the same search box and "show N more"
    // footer every other long surface in the app uses, so the four-device case
    // and the four-hundred-device case are the same code.
    renderFleetList() {
        const state = this.pagedLists.get('fleetDeviceList');
        if (state && document.getElementById('fleetDeviceListBody')) {
            // A refresh replaces the rows without disturbing the search term or
            // how far the reader has paged.
            state.items = this.fleet.devices;
            state.total = this.fleet.total;
            state.grandTotal = this.fleet.configured;
            this.paintPagedList('fleetDeviceList');
            return;
        }

        this.pagedLists.delete('fleetDeviceList');
        this.renderPagedList('fleetDeviceList', {
            items: this.fleet.devices,
            total: this.fleet.total,
            grandTotal: this.fleet.configured,
            noun: 'devices',
            placeholder: 'Search devices by name, address or state…',
            // The fleet lives on the server. Searching, filtering and paging are
            // asked of it rather than of an array here, so a browser holds the
            // page on screen instead of every configured device.
            source: async () => {
                const data = await this.fetchFleetPage();
                this.fleet.devices = Array.isArray(data.devices) ? data.devices : [];
                this.fleet.total = data.total || 0;
                this.fleet.configured = data.configured || 0;
                this.fleet.summary = data.summary || {};
                this.fleet.error = null;
                this.renderFleetHeader();
                this.renderFleetSummary();
                return {
                    items: this.fleet.devices,
                    total: this.fleet.total,
                    grandTotal: this.fleet.configured
                };
            },
            // The fleet overview always offers the search box.
            alwaysControls: true,
            sortColumns: this.fleetSortColumns(),
            sortKey: 'state',
            sortDir: 'asc',
            wrap: (rows, mountId) => `<table class="fleet-table">
                    <thead><tr>${this.sortHeadersHtml(mountId)}</tr></thead>
                    <tbody>${rows}</tbody>
                </table>`,
            renderRows: items => items.map(device => this.renderFleetRow(device)).join(''),
            // Rows are keyed by device so a repaint edits cells in place.
            rowKey: device => device.id,
            patchRow: (row, device) => this.patchFleetRow(row, device),
            // An empty fleet and a fleet that could not be read are different
            // facts, and only one of them is about configuration.
            emptyHtml: this.fleet.error
                ? `<div class="empty-state">
                       <h3>Device status is unavailable</h3>
                       <p>Nothing is being claimed about the fleet until the status endpoint answers.</p>
                   </div>`
                : `<div class="empty-state">
                       <h3>No devices configured</h3>
                       <p>Add a device on the Devices tab and it will start appearing here.</p>
                   </div>`
        });
    }

    // Fleet table columns in screen order; state sorts by severity, times and counts open descending.
    fleetSortColumns() {
        return [
            {
                key: 'device', label: 'Device', firstDir: 'asc',
                get: device => device.name || ''
            },
            {
                key: 'state', label: 'State', firstDir: 'asc',
                get: device => {
                    const key = this.fleetStateMeta(device.state).key;
                    const rank = FLEET_SEVERITY.indexOf(key);
                    // An unrecognised state sorts with the other unknowns
                    // rather than ahead of everything as -1 would put it.
                    return rank === -1 ? FLEET_SEVERITY.length : rank;
                }
            },
            {
                key: 'last_poll', label: 'Last poll', firstDir: 'desc',
                get: device => this.fleetTime((device.status || {}).last_poll_time)
            },
            {
                key: 'next_poll', label: 'Next poll', firstDir: 'asc',
                className: 'fleet-col-next',
                get: device => this.fleetTime((device.status || {}).next_poll_time)
            },
            {
                key: 'metrics', label: 'Metrics', firstDir: 'desc',
                className: 'fleet-col-metrics',
                get: device => {
                    const value = (device.status || {}).metrics_collected;
                    return typeof value === 'number' ? value : null;
                }
            },
            // Detail is a sentence and a control, not a value to order by.
            { key: 'detail', label: 'Detail' }
        ];
    }

    fleetSearchText(device) {
        const meta = this.fleetStateMeta(device.state);
        return `${device.name || ''} ${device.address || ''} ${meta.key} ${meta.label}`.toLowerCase();
    }

    // patchFleetRow updates only the cells of one row whose markup changed.
    patchFleetRow(row, device) {
        const meta = this.fleetStateMeta(device.state);
        const status = device.status || {};
        const metrics = status.metrics_collected;

        this.setCellHtml(row, '.fleet-device', `
                    <button type="button" class="fleet-open"
                            data-fleet-device="${this.escapeAttr(device.id)}">${this.escapeHtml(device.name)}</button>
                    <span class="fleet-address">${this.escapeHtml(device.address || '')}</span>
                `);
        this.setCellHtml(row, 'td:nth-child(2)', `
                    <span class="fleet-state fleet-tone--${this.escapeAttr(meta.tone)}">
                        <span class="fleet-dot"></span>${this.escapeHtml(meta.label)}
                    </span>
                `);
        this.setCellHtml(row, 'td:nth-child(3)', this.fleetTimeCell(status.last_poll_time, 'never'));
        this.setCellHtml(row, '.fleet-col-next', this.fleetTimeCell(status.next_poll_time, 'not scheduled'));
        this.setCellHtml(row, '.fleet-col-metrics',
            typeof metrics === 'number' ? metrics.toLocaleString() : '—');
        this.setCellHtml(row, 'td:last-child', this.fleetReasonHtml(device, meta));
    }

    renderFleetRow(device) {
        const meta = this.fleetStateMeta(device.state);
        const status = device.status || {};
        const metrics = status.metrics_collected;

        return `
            <tr class="fleet-row" data-row-key="${this.escapeAttr(device.id)}"
                data-fleet-device="${this.escapeAttr(device.id)}">
                <td class="fleet-device">
                    <button type="button" class="fleet-open"
                            data-fleet-device="${this.escapeAttr(device.id)}">${this.escapeHtml(device.name)}</button>
                    <span class="fleet-address">${this.escapeHtml(device.address || '')}</span>
                </td>
                <td>
                    <span class="fleet-state fleet-tone--${this.escapeAttr(meta.tone)}">
                        <span class="fleet-dot"></span>${this.escapeHtml(meta.label)}
                    </span>
                </td>
                <td>${this.fleetTimeCell(status.last_poll_time, 'never')}</td>
                <td class="fleet-col-next">${this.fleetTimeCell(status.next_poll_time, 'not scheduled')}</td>
                <td class="fleet-col-metrics fleet-metrics">${
                    typeof metrics === 'number' ? metrics.toLocaleString() : '—'
                }</td>
                <td>${this.fleetReasonHtml(device, meta)}</td>
            </tr>
        `;
    }

    // The state reason shows one line that opens the full message in the description sheet.
    fleetReasonHtml(device, meta) {
        const status = device.status || {};
        const failures = Number(status.consecutive_failures || 0);
        const detail = String(status.last_error || device.error || '').trim();

        if (!detail) {
            if (meta.key === 'ok') return '<span class="fleet-reason-note">—</span>';
            return `<span class="fleet-reason-note">${this.escapeHtml(meta.blurb)}</span>`;
        }

        const badge = failures > 0
            ? `<span class="badge error">${failures.toLocaleString()} failed poll${failures === 1 ? '' : 's'} in a row</span>`
            : '';

        return `<div class="fleet-reason">
                ${badge}
                ${this.descriptionTriggerHtml(detail, {
                    title: device.name,
                    eyebrow: meta.label,
                    className: 'desc-clamp',
                    meta: [
                        ['Address', device.address],
                        ['State', meta.label],
                        ['What the state means', meta.blurb],
                        ['Consecutive failures', failures > 0 ? failures.toLocaleString() : ''],
                        ['Last poll', this.fleetTimeText(status.last_poll_time, 'never')],
                    ],
                })}
            </div>`;
    }

    // An unrecognised state is carried through as itself. It is not healthy and
    // it is not down, and guessing which would be worse than saying so.
    fleetStateMeta(state) {
        const key = String(state == null ? '' : state).trim();
        if (FLEET_STATES[key]) return { key, ...FLEET_STATES[key] };
        if (key === '') return { key: 'unknown', ...FLEET_STATES.unknown };
        return {
            key,
            label: key,
            tone: 'unknown',
            blurb: 'This build does not recognise that state.'
        };
    }

    // ---- time ----------------------------------------------------------

    // The instant a Go time.Time field refers to, or null when the field is the
    // zero time that an ineffective omitempty leaves in the payload.
    fleetTime(value) {
        if (value === null || value === undefined || value === '') return null;
        const ms = typeof value === 'number' ? value : Date.parse(value);
        if (!Number.isFinite(ms) || ms < ZERO_TIME_CEILING) return null;
        return ms;
    }

    // "3m ago" answers the question a timestamp only lets you compute, and "in
    // 27s" does the same for a poll that has not happened yet.
    relativeTime(value, now = Date.now()) {
        const ms = this.fleetTime(value);
        if (ms === null) return null;

        const delta = ms - now;
        const magnitude = Math.abs(delta);
        if (magnitude < 5000) return 'just now';

        const units = [['d', 86400000], ['h', 3600000], ['m', 60000], ['s', 1000]];
        let span = '';
        for (const [suffix, size] of units) {
            if (magnitude >= size || suffix === 's') {
                span = `${Math.floor(magnitude / size)}${suffix}`;
                break;
            }
        }
        return delta < 0 ? `${span} ago` : `in ${span}`;
    }

    fleetTimeText(value, nullText) {
        return this.relativeTime(value) || nullText;
    }

    // The exact instant stays reachable in the title, for anyone correlating a
    // poll with a log, but it is not what the column reads as.
    fleetTimeCell(value, nullText) {
        const ms = this.fleetTime(value);
        if (ms === null) return `<span class="fleet-never">${this.escapeHtml(nullText)}</span>`;

        return `<span class="fleet-when" title="${this.escapeAttr(new Date(ms).toISOString())}">`
            + `${this.escapeHtml(this.relativeTime(ms))}</span>`;
    }

    // ---- interaction ---------------------------------------------------

    bindFleetOverview() {
        if (this.fleetBound) return;

        document.addEventListener('click', (event) => {
            const chip = event.target.closest('.fleet-chip');
            if (chip) {
                this.setFleetStateFilter(chip.dataset.fleetState || '');
                return;
            }
            // The device name is the control; the rest of the row follows it,
            // except where the row carries a control of its own.
            const opener = event.target.closest('.fleet-open');
            if (opener) {
                this.openFleetDevice(opener.dataset.fleetDevice);
                return;
            }
            const row = event.target.closest('.fleet-row');
            if (row && !event.target.closest('button, a, input, select')) {
                this.openFleetDevice(row.dataset.fleetDevice);
            }
        });

        // Coming back to a backgrounded tab, where the refresh tick has been
        // throttled or skipped, reads the fleet again rather than leaving a
        // stale answer to a question about right now.
        document.addEventListener('visibilitychange', () => {
            if (document.visibilityState === 'visible' && this.fleetVisible()) {
                this.refreshFleetStatus();
            }
        });

        this.fleetBound = true;
    }

    // A chip filters the fleet to one state by fetching a new first page; clicking it again clears.
    setFleetStateFilter(stateKey) {
        this.fleet.stateFilter = this.fleet.stateFilter === stateKey ? '' : stateKey;

        // A chip narrows the set, so the list is read from its start again.
        const state = this.pagedLists.get('fleetDeviceList');
        if (state) state.page = 0;

        this.renderFleetSummary();
        this.repagePagedList('fleetDeviceList');
    }

    // Opening a device goes through the selector, so the one place that knows
    // how to build a device dashboard stays the only place that does it.
    openFleetDevice(deviceId) {
        const select = document.getElementById('dashboardDeviceSelect');
        if (!select || !deviceId) return;

        select.value = String(deviceId);
        select.dispatchEvent(new Event('change'));
    }

    renderDevicesTable(devices) {
        this.renderPagedList('devicesList', {
            items: devices || [],
            noun: 'devices',
            placeholder: 'Search devices by name or address…',
            matches: (device, term) =>
                `${device.name || ''} ${device.address || ''}`.toLowerCase().includes(term),
            wrap: rows => `<table><thead><tr><th>Name</th><th>Address</th><th>Port</th>`
                + `<th>SNMP Version</th><th>Polling</th><th>Actions</th></tr></thead><tbody>${rows}</tbody></table>`,
            renderRows: items => items.map(device => `<tr>
                <td><strong>${this.escapeHtml(device.name)}</strong></td>
                <td>${this.escapeHtml(device.address)}</td>
                <td>${device.port || 161}</td>
                <td><span class="badge success">${device.snmp_version || 'v2c'}</span></td>
                <td>${device.enabled === false
                    ? '<span class="badge disabled">Disabled</span>'
                    : '<span class="badge enabled">Enabled</span>'}</td>
                <td class="actions">
                    <button class="secondary-btn" onclick="app.showDeviceDetails('${device.id}')">View</button>
                    <button class="secondary-btn" onclick="app.showEditDeviceModal('${device.id}')">Edit</button>
                    <button class="danger-btn" onclick="app.deleteDevice('${device.id}')">Delete</button>
                </td>
            </tr>`).join(''),
            emptyHtml: `<div class="empty-state">
                    <h3>No devices configured</h3>
                    <p>Add a device to start polling it.</p>
                </div>`
        });
    }

    async showDeviceDetails(deviceId) {
        try {
            const response = await fetch(`/api/devices/${deviceId}?stored=true`);
            const device = await response.json();
            this.currentDevice = device;

            document.getElementById('deviceDetailsTitle').textContent = device.name;
            this.renderDeviceInfo(device);

            // Load associations
            await this.loadDeviceProfiles(deviceId);
            await this.loadDeviceMIBs(deviceId);

            // Hide any previous SNMP test results
            this.hideSnmpTestResults();

            this.switchView('deviceDetails');
        } catch (error) {
            console.error('Error loading device details:', error);
            this.showNotification('Failed to load device details', 'error');
        }
    }

    async testSNMPConnection(deviceId) {
        const resultsContainer = document.getElementById('snmpTestResults');

        // Show loading state
        resultsContainer.className = 'snmp-test-results loading';
        resultsContainer.innerHTML = `
            <div class="test-header">
                <div class="test-title">
                    <span class="spinner"></span>
                    Testing SNMP Connection...
                </div>
            </div>
            <div class="test-message">Attempting to connect to device and retrieve system information...</div>
        `;
        resultsContainer.style.display = 'block';

        try {
            const response = await fetch(`/api/devices/${deviceId}/test-snmp`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' }
            });

            const result = await response.json();

            if (result.success) {
                this.showSnmpTestSuccess(result);
            } else {
                this.showSnmpTestError(result);
            }
        } catch (error) {
            console.error('SNMP test error:', error);
            this.showSnmpTestError({
                success: false,
                message: 'Failed to perform SNMP test',
                error: error.message || 'Network error - could not reach the WebUI server'
            });
        }
    }

    showSnmpTestSuccess(result) {
        const resultsContainer = document.getElementById('snmpTestResults');
        resultsContainer.className = 'snmp-test-results success';

        let systemInfoHtml = '';
        if (result.sys_name || result.sys_descr || result.sys_uptime || result.sys_location || result.sys_contact) {
            systemInfoHtml = '<div class="system-info">';

            if (result.sys_name) {
                systemInfoHtml += `
                    <div class="info-item">
                        <div class="info-label">System Name</div>
                        <div class="info-value">${this.escapeHtml(result.sys_name)}</div>
                    </div>
                `;
            }
            if (result.sys_descr) {
                systemInfoHtml += `
                    <div class="info-item">
                        <div class="info-label">System Description</div>
                        <div class="info-value">${this.escapeHtml(result.sys_descr)}</div>
                    </div>
                `;
            }
            if (result.sys_uptime) {
                systemInfoHtml += `
                    <div class="info-item">
                        <div class="info-label">Uptime</div>
                        <div class="info-value">${this.escapeHtml(result.sys_uptime)}</div>
                    </div>
                `;
            }
            if (result.sys_location) {
                systemInfoHtml += `
                    <div class="info-item">
                        <div class="info-label">Location</div>
                        <div class="info-value">${this.escapeHtml(result.sys_location)}</div>
                    </div>
                `;
            }
            if (result.sys_contact) {
                systemInfoHtml += `
                    <div class="info-item">
                        <div class="info-label">Contact</div>
                        <div class="info-value">${this.escapeHtml(result.sys_contact)}</div>
                    </div>
                `;
            }

            systemInfoHtml += '</div>';
        }

        resultsContainer.innerHTML = `
            <div class="test-header">
                <div class="test-title">
                    \u2705 SNMP Connection Successful
                </div>
                <div style="display: flex; align-items: center; gap: 15px;">
                    <span class="test-duration">${result.duration}</span>
                    <button class="dismiss-btn" onclick="app.hideSnmpTestResults()" title="Dismiss">&times;</button>
                </div>
            </div>
            <div class="test-message">${result.message}</div>
            ${systemInfoHtml}
        `;
    }

    showSnmpTestError(result) {
        const resultsContainer = document.getElementById('snmpTestResults');
        resultsContainer.className = 'snmp-test-results error';

        resultsContainer.innerHTML = `
            <div class="test-header">
                <div class="test-title">
                    \u274C SNMP Connection Failed
                </div>
                <div style="display: flex; align-items: center; gap: 15px;">
                    ${result.duration ? `<span class="test-duration">${result.duration}</span>` : ''}
                    <button class="dismiss-btn" onclick="app.hideSnmpTestResults()" title="Dismiss">&times;</button>
                </div>
            </div>
            <div class="test-message">${result.message || 'Connection test failed'}</div>
            ${result.error ? `<div class="test-error">${this.escapeHtml(result.error)}</div>` : ''}
        `;
    }

    hideSnmpTestResults() {
        const resultsContainer = document.getElementById('snmpTestResults');
        resultsContainer.style.display = 'none';
    }

    renderDeviceInfo(device) {
        const container = document.getElementById('deviceInfo');
        container.innerHTML = `
            <div class="info-grid">
                <div class="info-item">
                    <label>Name:</label>
                    <span>${this.escapeHtml(device.name)}</span>
                </div>
                <div class="info-item">
                    <label>Address:</label>
                    <span>${this.escapeHtml(device.address)}</span>
                </div>
                <div class="info-item">
                    <label>Port:</label>
                    <span>${device.port || 161}</span>
                </div>
                <div class="info-item">
                    <label>SNMP Version:</label>
                    <span>${device.snmp_version || 'v2c'}</span>
                </div>
                <div class="info-item">
                    <label>Community:</label>
                    <span>${this.escapeHtml(device.community || 'public')}</span>
                </div>
            </div>
        `;
    }

    async loadDeviceProfiles(deviceId) {
        try {
            const response = await fetch(`/api/devices/${deviceId}/profiles`);
            const data = await response.json();
            const profiles = data.profiles || [];

            // Cached for the "All applied MIBs" view, which needs to point a
            // profile-sourced MIB back at the profile that owns it.
            this.currentDeviceProfiles = profiles;

            this.renderPagedList('deviceProfiles', {
                items: profiles,
                noun: 'profiles',
                placeholder: 'Search assigned profiles…',
                matches: (profile, term) => (profile.name || '').toLowerCase().includes(term),
                renderRows: items => items.map(profile => `
                        <div class="list-item">
                            <span>${this.escapeHtml(profile.name)}</span>
                            <button class="danger-btn" onclick="app.removeProfileFromDevice('${deviceId}', '${profile.id}')">Remove</button>
                        </div>
                    `).join(''),
                emptyHtml: '<p class="empty-state">No profiles assigned</p>'
            });
        } catch (error) {
            console.error('Error loading device profiles:', error);
        }
    }

    async loadDeviceMIBs(deviceId) {
        try {
            const response = await fetch(`/api/devices/${deviceId}/mibs`);
            const data = await response.json();
            const directMibs = data.direct_mibs || [];
            const allMibs = data.all_mibs || [];

            // Cached so the toggle/filter controls can look up the assignment
            // that is already embedded in the listing, instead of an extra
            // GET per row.
            this.currentDeviceMibsData = { direct: directMibs, all: allMibs };
            const directIds = new Set(directMibs.map(m => m.id));

            // Direct MIBs — editable: these are the assignments a PUT here
            // actually changes.
            this.renderPagedList('deviceDirectMibs', {
                items: directMibs,
                noun: 'modules',
                placeholder: 'Search directly assigned modules…',
                matches: (mib, term) => (mib.name || '').toLowerCase().includes(term),
                renderRows: items => items.map(mib => this.renderMibAssignmentRow('device', deviceId, mib, {
                    onRemove: `app.removeMIBFromDevice('${deviceId}', '${mib.id}')`
                })).join(''),
                emptyHtml: '<p class="empty-state">No direct MIB assignments</p>'
            });

            // All applied MIBs — the effective set (direct beats profile).
            // Read-only here: a profile-sourced MIB can only be edited on the
            // profile that owns it.
            this.renderPagedList('deviceAllMibs', {
                items: allMibs,
                noun: 'modules',
                placeholder: 'Search applied modules…',
                matches: (mib, term) => (mib.name || '').toLowerCase().includes(term),
                renderRows: items => items.map(mib => this.renderEffectiveMibRow(deviceId, mib, directIds)).join(''),
                emptyHtml: '<p class="empty-state">No MIBs applied (assign profiles or MIBs)</p>'
            });
        } catch (error) {
            console.error('Error loading device MIBs:', error);
        }
    }

    showAddDeviceModal() {
        document.getElementById('deviceModalTitle').textContent = 'Add Device';
        document.getElementById('deviceForm').reset();
        document.querySelector('#deviceForm input[name="id"]').value = '';
        // Enable name field for new devices
        document.querySelector('#deviceForm input[name="name"]').readOnly = false;
        this.syncDeviceVersionFields();
        this.showModal('deviceModal');
    }

    async showEditDeviceModal(deviceId) {
        try {
            const response = await fetch(`/api/devices/${deviceId}?stored=true`);
            const device = await response.json();

            document.getElementById('deviceModalTitle').textContent = 'Edit Device';
            const form = document.getElementById('deviceForm');
            form.querySelector('input[name="id"]').value = device.id;
            form.querySelector('input[name="name"]').value = device.name;
            form.querySelector('input[name="address"]').value = device.address;
            form.querySelector('select[name="snmp_version"]').value = device.snmp_version || 'v2c';
            form.querySelector('input[name="community"]').value = device.community || 'public';
            form.querySelector('input[name="port"]').value = device.port || 161;
            // Absent is enabled: a device stored before the field existed is polled.
            form.querySelector('input[name="enabled"]').checked = device.enabled !== false;

            form.querySelector('input[name="username"]').value = device.username || '';
            form.querySelector('select[name="auth_protocol"]').value = device.auth_protocol || 'SHA';
            form.querySelector('select[name="priv_protocol"]').value = device.priv_protocol || 'AES';
            // The password fields stay empty. The stored secret is not put back
            // into the page, and blank means "unchanged" on save.
            form.querySelector('input[name="auth_password"]').value = '';
            form.querySelector('input[name="priv_password"]').value = '';

            this.syncDeviceVersionFields();

            // readOnly, not disabled: a disabled input is left out of FormData.
            form.querySelector('input[name="name"]').readOnly = true;

            this.showModal('deviceModal');
        } catch (error) {
            console.error('Error loading device for edit:', error);
            this.showNotification('Failed to load device', 'error');
        }
    }

    // Show only the credentials the chosen SNMP version uses.
    syncDeviceVersionFields() {
        const form = document.getElementById('deviceForm');
        if (!form) return;

        const version = form.querySelector('select[name="snmp_version"]').value;
        const isV3 = version === 'v3';

        const community = document.getElementById('deviceCommunityFields');
        const v3 = document.getElementById('deviceV3Fields');
        if (community) community.hidden = isV3;
        if (v3) v3.hidden = !isV3;

        // required is set here rather than in the markup: a hidden required
        // field blocks submission with a message pointing at something the
        // reader cannot see.
        const username = form.querySelector('input[name="username"]');
        if (username) username.required = isV3;
        const communityInput = form.querySelector('input[name="community"]');
        if (communityInput) communityInput.required = !isV3;
    }

    async saveDevice(formData) {
        const deviceId = formData.get('id');
        const isEdit = deviceId && deviceId.length > 0;

        const version = formData.get('snmp_version');
        const device = {
            name: formData.get('name'),
            address: formData.get('address'),
            snmp_version: version,
            port: parseInt(formData.get('port')),
            // An unchecked checkbox is absent from FormData, so absent here
            // is "off", and the field is always sent: on an update an omitted
            // enabled means enabled.
            enabled: formData.get('enabled') !== null,
        };

        // Only the credentials the chosen version uses are sent. Sending a
        // community string with a v3 device would store one that nothing reads
        // and that an export would then carry around as a secret.
        if (version === 'v3') {
            device.username = formData.get('username') || '';
            device.auth_protocol = formData.get('auth_protocol') || '';
            device.priv_protocol = formData.get('priv_protocol') || '';

            // A blank password on an edit keeps the stored one.
            const auth = formData.get('auth_password');
            const priv = formData.get('priv_password');
            if (auth) device.auth_password = auth;
            if (priv) device.priv_password = priv;
        } else {
            device.community = formData.get('community');
        }

        if (isEdit) {
            // storage.Device.ID is an int64, so send a number.
            device.id = Number(deviceId);
        }

        try {
            const url = isEdit ? `/api/devices/${deviceId}?stored=true` : '/api/devices?stored=true';
            const method = isEdit ? 'PUT' : 'POST';

            const response = await fetch(url, {
                method: method,
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(device)
            });

            if (!response.ok) {
                throw new Error(`HTTP error! status: ${response.status}`);
            }

            this.showNotification(`Device ${isEdit ? 'updated' : 'created'} successfully`, 'success');
            this.closeModal('deviceModal');
            await this.loadStoredDevices();
        } catch (error) {
            console.error('Error saving device:', error);
            this.showNotification(`Failed to ${isEdit ? 'update' : 'create'} device`, 'error');
        }
    }

    async deleteDevice(deviceId) {
        if (!await this.confirm('Are you sure you want to delete this device?')) {
            return;
        }

        try {
            const response = await fetch(`/api/devices/${deviceId}?stored=true`, {
                method: 'DELETE'
            });

            if (!response.ok) {
                // Show the server's reason a delete did not happen.
                const reason = (await response.text()).trim();
                throw new Error(reason || `HTTP error! status: ${response.status}`);
            }

            this.showNotification('Device deleted successfully', 'success');
            await this.loadStoredDevices();
        } catch (error) {
            console.error('Error deleting device:', error);
            this.showNotification(`Failed to delete device: ${error.message}`, 'error', 6000);
        }
    }

    // ========================================
    // Profile Management
    // ========================================

    // strict: true reports a failed read instead of swallowing it -- see
    // loadDevices. An error response carrying valid JSON is a failure too: it
    // parses to no `profiles` key, which reads as "there are none".
    async loadProfiles({ strict = false } = {}) {
        try {
            const response = await fetch('/api/profiles');
            if (!response.ok) throw new Error(`HTTP ${response.status}`);
            const data = await response.json();
            this.profiles = data.profiles || [];
            this.renderProfilesTable(this.profiles);
        } catch (error) {
            console.error('Error loading profiles:', error);
            if (strict) throw error;
            this.showNotification('Failed to load profiles', 'error');
        }
    }

    renderProfilesTable(profiles) {
        this.renderPagedList('profilesList', {
            items: profiles || [],
            noun: 'profiles',
            placeholder: 'Search profiles by name or description…',
            matches: (profile, term) =>
                `${profile.name || ''} ${profile.description || ''}`.toLowerCase().includes(term),
            wrap: rows => '<table><thead><tr><th>Name</th><th>Description</th>'
                + `<th>Actions</th></tr></thead><tbody>${rows}</tbody></table>`,
            renderRows: items => items.map(profile => `<tr>
                <td><strong>${this.escapeHtml(profile.name)}</strong></td>
                <td>${this.descriptionCellHtml(profile.description, profile.name, 'Profile')}</td>
                <td class="actions">
                    <button class="secondary-btn" onclick="app.showProfileDetails('${profile.id}')">View</button>
                    <button class="secondary-btn" onclick="app.showEditProfileModal('${profile.id}')">Edit</button>
                    <button class="danger-btn" onclick="app.deleteProfile('${profile.id}')">Delete</button>
                </td>
            </tr>`).join(''),
            emptyHtml: `<div class="empty-state">
                    <h3>No profiles configured</h3>
                    <p>A profile is a reusable bundle of MIBs you can apply to many devices.</p>
                </div>`
        });
    }

    async showProfileDetails(profileId) {
        try {
            const response = await fetch(`/api/profiles/${profileId}`);
            const profile = await response.json();
            this.currentProfile = profile;

            document.getElementById('profileDetailsTitle').textContent = profile.name;
            this.renderProfileInfo(profile);

            await this.loadProfileMIBs(profileId);
            await this.loadProfileDevices(profileId);

            this.switchView('profileDetails');
        } catch (error) {
            console.error('Error loading profile details:', error);
            this.showNotification('Failed to load profile details', 'error');
        }
    }

    renderProfileInfo(profile) {
        const container = document.getElementById('profileInfo');
        container.innerHTML = `
            <div class="info-grid">
                <div class="info-item">
                    <label>Name:</label>
                    <span>${this.escapeHtml(profile.name)}</span>
                </div>
                <div class="info-item">
                    <label>Description:</label>
                    <span>${this.escapeHtml(profile.description || 'N/A')}</span>
                </div>
            </div>
        `;
    }

    async loadProfileMIBs(profileId) {
        try {
            const response = await fetch(`/api/profiles/${profileId}/mibs`);
            const data = await response.json();
            const mibs = data.mibs || [];

            // Cached for the toggle/filter controls, same reasoning as
            // currentDeviceMibsData above.
            this.currentProfileMibsData = { mibs };

            this.renderPagedList('profileMibs', {
                items: mibs,
                noun: 'modules',
                placeholder: 'Search assigned modules…',
                matches: (mib, term) => (mib.name || '').toLowerCase().includes(term),
                renderRows: items => items.map(mib => this.renderMibAssignmentRow('profile', profileId, mib, {
                    onRemove: `app.removeMIBFromProfile('${profileId}', '${mib.id}')`
                })).join(''),
                emptyHtml: '<p class="empty-state">No MIBs assigned</p>'
            });
        } catch (error) {
            console.error('Error loading profile MIBs:', error);
        }
    }

    async loadProfileDevices(profileId) {
        try {
            const response = await fetch(`/api/profiles/${profileId}/devices`);
            const data = await response.json();
            const devices = data.devices || [];

            this.renderPagedList('profileDevices', {
                items: devices,
                noun: 'devices',
                placeholder: 'Search devices using this profile…',
                matches: (device, term) => (device.name || '').toLowerCase().includes(term),
                renderRows: items => items.map(device => `
                        <div class="list-item">
                            <span>${this.escapeHtml(device.name)}</span>
                            <button class="secondary-btn" onclick="app.showDeviceDetails('${device.id}')">View</button>
                        </div>
                    `).join(''),
                emptyHtml: '<p class="empty-state">No devices using this profile</p>'
            });
        } catch (error) {
            console.error('Error loading profile devices:', error);
        }
    }

    showAddProfileModal() {
        document.getElementById('profileModalTitle').textContent = 'Add Profile';
        document.getElementById('profileForm').reset();
        document.querySelector('#profileForm input[name="id"]').value = '';
        this.showModal('profileModal');
    }

    async showEditProfileModal(profileId) {
        try {
            const response = await fetch(`/api/profiles/${profileId}`);
            const profile = await response.json();

            document.getElementById('profileModalTitle').textContent = 'Edit Profile';
            const form = document.getElementById('profileForm');
            form.querySelector('input[name="id"]').value = profile.id;
            form.querySelector('input[name="name"]').value = profile.name;
            form.querySelector('textarea[name="description"]').value = profile.description || '';

            this.showModal('profileModal');
        } catch (error) {
            console.error('Error loading profile for edit:', error);
            this.showNotification('Failed to load profile', 'error');
        }
    }

    async saveProfile(formData) {
        const profileId = formData.get('id');
        const isEdit = profileId && profileId.length > 0;

        const profile = {
            name: formData.get('name'),
            description: formData.get('description'),
        };

        if (isEdit) {
            profile.id = profileId;
        }

        try {
            const url = isEdit ? `/api/profiles/${profileId}` : '/api/profiles';
            const method = isEdit ? 'PUT' : 'POST';

            const response = await fetch(url, {
                method: method,
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(profile)
            });

            if (!response.ok) {
                throw new Error(`HTTP error! status: ${response.status}`);
            }

            this.showNotification(`Profile ${isEdit ? 'updated' : 'created'} successfully`, 'success');
            this.closeModal('profileModal');
            await this.loadProfiles();
        } catch (error) {
            console.error('Error saving profile:', error);
            this.showNotification(`Failed to ${isEdit ? 'update' : 'create'} profile`, 'error');
        }
    }

    async deleteProfile(profileId) {
        if (!await this.confirm('Are you sure you want to delete this profile?')) {
            return;
        }

        try {
            const response = await fetch(`/api/profiles/${profileId}`, {
                method: 'DELETE'
            });

            if (!response.ok) {
                throw new Error(`HTTP error! status: ${response.status}`);
            }

            this.showNotification('Profile deleted successfully', 'success');
            await this.loadProfiles();
        } catch (error) {
            console.error('Error deleting profile:', error);
            this.showNotification('Failed to delete profile', 'error');
        }
    }

    // ========================================
    // MIB Management
    // ========================================

    async loadStoredMIBs({ strict = false } = {}) {
        try {
            const response = await fetch('/api/mibs');
            if (!response.ok) throw new Error(`HTTP ${response.status}`);
            const data = await response.json();
            this.mibs = data.mibs || [];
            this.renderStoredMIBsTable(this.mibs);
        } catch (error) {
            console.error('Error loading stored MIBs:', error);
            if (strict) throw error;
            this.showNotification('Failed to load MIBs', 'error');
        }
    }

    // Headline totals for the MIB library, derived from the list payload the
    // table already fetched. No extra request, no extra endpoint.
    renderMIBLibrarySummary(mibs) {
        const container = document.getElementById('mibSummaryCards');
        if (!container) return;

        const list = mibs || [];
        const objects = list.reduce((sum, mib) => sum + (mib.object_count || 0), 0);
        // A module with no objects is a textual-convention or conformance module,
        // which is a correct import rather than a parse failure.
        const definitionsOnly = list.filter(mib => (mib.object_count || 0) === 0).length;
        const withObjects = list.length - definitionsOnly;

        container.innerHTML = [
            this.renderSummaryCard('MIB modules', list.length,
                list.length === 0 ? 'Nothing imported yet' : 'Imported and available to assign'),
            this.renderSummaryCard('SNMP objects', objects,
                'Resolved across every imported module'),
            this.renderSummaryCard('Modules with objects', withObjects,
                definitionsOnly === 0
                    ? 'Every module declares at least one object'
                    : `${definitionsOnly} carr${definitionsOnly === 1 ? 'ies' : 'y'} textual conventions or conformance groups only`),
        ].join('');
    }

    renderSummaryCard(label, value, hint) {
        return `
            <div class="summary-card">
                <span class="summary-card-label">${this.escapeHtml(label)}</span>
                <span class="summary-card-value">${value.toLocaleString()}</span>
                <span class="summary-card-hint">${this.escapeHtml(hint)}</span>
            </div>
        `;
    }

    renderStoredMIBsTable(mibs) {
        this.renderMIBLibrarySummary(mibs);

        // ~170 modules, so the table is searched by name or tag and paged the
        // same way every other long list in the app is.
        this.renderPagedList('storedMibsList', {
            items: mibs || [],
            noun: 'modules',
            placeholder: 'Search modules by name or tag…',
            matches: (mib, term) =>
                `${mib.name || ''} ${(mib.tags || []).join(' ')}`.toLowerCase().includes(term),
            wrap: rows => '<table><thead><tr><th>Name</th><th>Tags</th><th>Object Count</th>'
                + `<th>Actions</th></tr></thead><tbody>${rows}</tbody></table>`,
            renderRows: items => items.map(mib => `<tr>
                <td><strong>${this.escapeHtml(mib.name)}</strong></td>
                <td class="tags-cell">
                    ${this.renderMibTags(mib)}
                    <button class="add-tag-btn" onclick="app.showEditTagsModal(${mib.id}, '${this.escapeHtml(mib.name)}', ${JSON.stringify(mib.tags || []).replace(/"/g, '&quot;')})">+ tag</button>
                </td>
                <td>${(mib.object_count || 0).toLocaleString()} object${(mib.object_count || 0) === 1 ? '' : 's'}</td>
                <td class="actions">
                    <button class="danger-btn" onclick="app.deleteMIB('${mib.id}')">Delete</button>
                </td>
            </tr>`).join(''),
            emptyHtml: `<div class="empty-state">
                    <h3>No MIBs imported</h3>
                    <p>Import modules and they become available to assign to devices and profiles.</p>
                </div>`
        });
    }

    renderMibTags(mib) {
        if (!mib.tags || mib.tags.length === 0) {
            return '';
        }
        return mib.tags.map(tag =>
            `<span class="tag-badge">${this.escapeHtml(tag)}</span>`
        ).join('');
    }

    async handleMIBUpload(file, tags = []) {
        if (!file) return;

        const formData = new FormData();
        formData.append('file', file);
        if (tags && tags.length > 0) {
            formData.append('tags', tags.join(','));
        }

        try {
            const response = await fetch('/api/mibs/import', {
                method: 'POST',
                body: formData
            });

            if (!response.ok) {
                throw new Error(`HTTP error! status: ${response.status}`);
            }

            return await response.json();
        } catch (error) {
            console.error('Error uploading MIB:', error);
            throw error;
        }
    }

    async handleBulkMIBUpload(files, tags = []) {
        const fileArray = Array.from(files);
        const total = fileArray.length;
        let successCount = 0;
        let failCount = 0;

        this.showNotification(`Uploading ${total} MIB file(s)...`, 'info');

        for (const file of fileArray) {
            try {
                await this.handleMIBUpload(file, tags);
                successCount++;
            } catch (error) {
                console.error(`Failed to upload ${file.name}:`, error);
                failCount++;
            }
        }

        if (failCount === 0) {
            this.showNotification(`Successfully uploaded ${successCount} MIB(s)`, 'success');
        } else {
            this.showNotification(`Uploaded ${successCount} MIB(s), ${failCount} failed`, 'warning');
        }

        // Refresh MIB list
        this.invalidateMIBBrowser();
        await this.loadStoredMIBs();
    }

    async deleteMIB(mibId) {
        if (!await this.confirm('Are you sure you want to delete this MIB?')) {
            return;
        }

        try {
            const response = await fetch(`/api/mibs/${mibId}`, {
                method: 'DELETE'
            });

            if (!response.ok) {
                throw new Error(`HTTP error! status: ${response.status}`);
            }

            this.showNotification('MIB deleted successfully', 'success');
            this.invalidateMIBBrowser();
            await this.loadStoredMIBs();
        } catch (error) {
            console.error('Error deleting MIB:', error);
            this.showNotification('Failed to delete MIB', 'error');
        }
    }

    // ========================================
    // MIB Import Dialog
    // ========================================

    setupMibImportModal() {
        const modal = document.getElementById('mibImportModal');
        const fileInput = document.getElementById('mibImportFileInput');
        const dropZone = document.getElementById('mibDropZone');
        const tagsInput = document.getElementById('mibImportTags');
        const submitBtn = document.getElementById('importMibSubmit');
        const cancelBtn = modal.querySelector('.cancel-btn');
        const closeBtn = modal.querySelector('.close-modal');

        // Track selected files
        this.selectedMibFiles = [];

        // File input change
        fileInput.addEventListener('change', (e) => {
            this.addFilesToSelection(Array.from(e.target.files));
            e.target.value = ''; // Reset so same files can be selected again
        });

        // Drag and drop
        dropZone.addEventListener('dragover', (e) => {
            e.preventDefault();
            dropZone.classList.add('drag-over');
        });

        dropZone.addEventListener('dragleave', () => {
            dropZone.classList.remove('drag-over');
        });

        dropZone.addEventListener('drop', (e) => {
            e.preventDefault();
            dropZone.classList.remove('drag-over');
            const files = Array.from(e.dataTransfer.files).filter(f =>
                f.name.endsWith('.mib') || f.name.endsWith('.txt') || f.name.endsWith('.my')
            );
            this.addFilesToSelection(files);
        });

        // Tags input - live preview
        tagsInput.addEventListener('input', () => {
            this.updateTagsPreview();
        });

        // Submit
        submitBtn.addEventListener('click', () => {
            this.submitMibImport();
        });

        // Cancel/Close
        cancelBtn.addEventListener('click', () => {
            this.closeMibImportModal();
        });

        closeBtn.addEventListener('click', () => {
            this.closeMibImportModal();
        });

        // Close on background click
        modal.addEventListener('click', (e) => {
            if (e.target === modal) {
                this.closeMibImportModal();
            }
        });
    }

    showMibImportModal() {
        // Reset state
        this.selectedMibFiles = [];
        document.getElementById('mibImportTags').value = '';
        document.getElementById('tagsPreview').innerHTML = '';
        document.getElementById('selectedFilesContainer').style.display = 'none';
        document.getElementById('selectedFilesList').innerHTML = '';
        document.getElementById('selectedFileCount').textContent = '0';
        document.getElementById('importMibSubmit').disabled = true;

        // Show modal
        this.showModal('mibImportModal');
    }

    closeMibImportModal() {
        this.closeModal('mibImportModal');
        this.selectedMibFiles = [];
    }

    addFilesToSelection(files) {
        for (const file of files) {
            // Check if file is already selected (by name)
            if (!this.selectedMibFiles.some(f => f.name === file.name)) {
                this.selectedMibFiles.push(file);
            }
        }
        this.updateSelectedFilesList();
    }

    removeFileFromSelection(fileName) {
        this.selectedMibFiles = this.selectedMibFiles.filter(f => f.name !== fileName);
        this.updateSelectedFilesList();
    }

    updateSelectedFilesList() {
        const container = document.getElementById('selectedFilesContainer');
        const list = document.getElementById('selectedFilesList');
        const countSpan = document.getElementById('selectedFileCount');
        const submitBtn = document.getElementById('importMibSubmit');

        if (this.selectedMibFiles.length === 0) {
            container.style.display = 'none';
            submitBtn.disabled = true;
            return;
        }

        container.style.display = 'block';
        countSpan.textContent = this.selectedMibFiles.length;
        submitBtn.disabled = false;

        list.innerHTML = this.selectedMibFiles.map(file => `
            <div class="selected-file-item">
                <span class="selected-file-name">${this.escapeHtml(file.name)}</span>
                <button class="selected-file-remove" onclick="app.removeFileFromSelection('${this.escapeHtml(file.name)}')">&times;</button>
            </div>
        `).join('');
    }

    updateTagsPreview() {
        const input = document.getElementById('mibImportTags');
        const preview = document.getElementById('tagsPreview');

        const tags = input.value.split(',')
            .map(t => t.trim())
            .filter(t => t.length > 0);

        if (tags.length === 0) {
            preview.innerHTML = '';
            return;
        }

        preview.innerHTML = tags.map(tag =>
            `<span class="tag-badge tag-preview">${this.escapeHtml(tag)}</span>`
        ).join('');
    }

    async submitMibImport() {
        if (this.selectedMibFiles.length === 0) {
            this.showNotification('Please select at least one MIB file', 'error');
            return;
        }

        // Parse tags
        const tagsInput = document.getElementById('mibImportTags').value;
        const tags = tagsInput.split(',')
            .map(t => t.trim())
            .filter(t => t.length > 0);

        // Save files before closing modal (closeMibImportModal clears selectedMibFiles)
        const filesToUpload = [...this.selectedMibFiles];

        // Close modal
        this.closeMibImportModal();

        // Upload files
        await this.handleBulkMIBUpload(filesToUpload, tags);
    }

    showEditTagsModal(mibId, mibName, currentTags) {
        const body = document.getElementById('modalOverlayBody');
        const title = document.getElementById('modalOverlayTitle');

        title.textContent = `Edit Tags: ${mibName}`;

        const tagsStr = (currentTags || []).join(', ');
        body.innerHTML = `
            <div style="padding: 20px;">
                <label style="display: block; margin-bottom: 15px;">
                    <span style="display: block; color: #94a3b8; margin-bottom: 8px;">Tags (comma-separated)</span>
                    <input type="text" id="editTagsInput" value="${this.escapeHtml(tagsStr)}"
                           style="width: 100%; padding: 10px 12px; background: #0f172a; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0;">
                </label>
                <div id="editTagsPreview" class="tags-preview" style="margin-bottom: 20px;"></div>
                <div class="modal-actions">
                    <button class="primary-btn" onclick="app.saveTagsEdit(${mibId})">Save</button>
                    <button class="secondary-btn" onclick="document.getElementById('modalOverlay').style.display='none'">Cancel</button>
                </div>
            </div>
        `;

        // Live preview
        const input = document.getElementById('editTagsInput');
        const preview = document.getElementById('editTagsPreview');

        const updatePreview = () => {
            const tags = input.value.split(',').map(t => t.trim()).filter(t => t.length > 0);
            preview.innerHTML = tags.map(tag =>
                `<span class="tag-badge tag-preview">${this.escapeHtml(tag)}</span>`
            ).join('');
        };

        input.addEventListener('input', updatePreview);
        updatePreview();

        document.getElementById('modalOverlay').style.display = 'flex';
    }

    async saveTagsEdit(mibId) {
        const input = document.getElementById('editTagsInput');
        const tags = input.value.split(',')
            .map(t => t.trim())
            .filter(t => t.length > 0);

        try {
            const response = await fetch(`/api/mibs/${mibId}/tags`, {
                method: 'PUT',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ tags })
            });

            if (!response.ok) {
                throw new Error('Failed to update tags');
            }

            this.showNotification('Tags updated successfully', 'success');
            document.getElementById('modalOverlay').style.display = 'none';
            await this.loadStoredMIBs();
        } catch (error) {
            console.error('Error updating tags:', error);
            this.showNotification('Failed to update tags', 'error');
        }
    }

    // ========================================
    // Associations
    // ========================================

    async assignProfileToDevice(deviceId, profileId) {
        try {
            const response = await fetch(`/api/devices/${deviceId}/profiles/${profileId}`, {
                method: 'POST'
            });

            if (!response.ok) {
                throw new Error(`HTTP error! status: ${response.status}`);
            }

            this.showNotification('Profile assigned successfully', 'success');
            await this.loadDeviceProfiles(deviceId);
            await this.loadDeviceMIBs(deviceId);
        } catch (error) {
            console.error('Error assigning profile:', error);
            this.showNotification('Failed to assign profile', 'error');
        }
    }

    async removeProfileFromDevice(deviceId, profileId) {
        if (!await this.confirm('Remove this profile from the device?')) {
            return;
        }

        try {
            const response = await fetch(`/api/devices/${deviceId}/profiles/${profileId}`, {
                method: 'DELETE'
            });

            if (!response.ok) {
                throw new Error(`HTTP error! status: ${response.status}`);
            }

            this.showNotification('Profile removed successfully', 'success');
            await this.loadDeviceProfiles(deviceId);
            await this.loadDeviceMIBs(deviceId);
        } catch (error) {
            console.error('Error removing profile:', error);
            this.showNotification('Failed to remove profile', 'error');
        }
    }

    async assignMIBToDevice(deviceId, mibId) {
        try {
            const response = await fetch(`/api/devices/${deviceId}/mibs`, {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json'
                },
                body: JSON.stringify({ mib_id: parseInt(mibId) })
            });

            if (!response.ok) {
                throw new Error(`HTTP error! status: ${response.status}`);
            }

            // Show what the assignment reports it will produce.
            const assignment = await response.json().catch(() => null);
            if (assignment?.warning) {
                this.showNotification(assignment.warning, 'warning', 12000);
            } else if (assignment?.series > 0) {
                this.showNotification(
                    `MIB assigned: about ${assignment.series.toLocaleString()} series from ` +
                    `${assignment.pollable.toLocaleString()} pollable objects`, 'success');
            } else {
                this.showNotification('MIB assigned successfully', 'success');
            }
            await this.loadDeviceMIBs(deviceId);
            
            // If this device is currently displayed in dashboard, reload dashboard tabs
            const currentDevice = document.getElementById('dashboardDeviceSelect')?.value;
            if (currentDevice && this.devices.find(d => d.id === deviceId)?.name === currentDevice) {
                await this.loadDeviceDashboards(deviceId);
            }
        } catch (error) {
            console.error('Error assigning MIB:', error);
            this.showNotification('Failed to assign MIB', 'error');
        }
    }

    async removeMIBFromDevice(deviceId, mibId) {
        if (!await this.confirm('Remove this MIB from the device?')) {
            return;
        }

        try {
            const response = await fetch(`/api/devices/${deviceId}/mibs/${mibId}`, {
                method: 'DELETE'
            });

            if (!response.ok) {
                throw new Error(`HTTP error! status: ${response.status}`);
            }

            this.showNotification('MIB removed successfully', 'success');
            await this.loadDeviceMIBs(deviceId);
            
            // If this device is currently displayed in dashboard, reload dashboard tabs
            const currentDevice = document.getElementById('dashboardDeviceSelect')?.value;
            if (currentDevice && this.devices.find(d => d.id === deviceId)?.name === currentDevice) {
                await this.loadDeviceDashboards(deviceId);
            }
        } catch (error) {
            console.error('Error removing MIB:', error);
            this.showNotification('Failed to remove MIB', 'error');
        }
    }

    async assignMIBToProfile(profileId, mibId) {
        try {
            const response = await fetch(`/api/profiles/${profileId}/mibs`, {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json'
                },
                body: JSON.stringify({ mib_id: parseInt(mibId) })
            });

            if (!response.ok) {
                throw new Error(`HTTP error! status: ${response.status}`);
            }

            this.showNotification('MIB assigned successfully', 'success');
            await this.loadProfileMIBs(profileId);
        } catch (error) {
            console.error('Error assigning MIB:', error);
            this.showNotification('Failed to assign MIB', 'error');
        }
    }

    async removeMIBFromProfile(profileId, mibId) {
        if (!await this.confirm('Remove this MIB from the profile?')) {
            return;
        }

        try {
            const response = await fetch(`/api/profiles/${profileId}/mibs/${mibId}`, {
                method: 'DELETE'
            });

            if (!response.ok) {
                throw new Error(`HTTP error! status: ${response.status}`);
            }

            this.showNotification('MIB removed successfully', 'success');
            await this.loadProfileMIBs(profileId);
        } catch (error) {
            console.error('Error removing MIB:', error);
            this.showNotification('Failed to remove MIB', 'error');
        }
    }

    // MIB polling controls; scope is 'device' or 'profile', sharing the same assignment endpoints.

    // findMibRecord looks up the AssignedMIB (which already carries the
    // current `assignment`) from the last list response, avoiding an extra
    // GET before every toggle or filter edit.
    findMibRecord(scope, mibId) {
        mibId = parseInt(mibId, 10);
        if (scope === 'device') {
            return (this.currentDeviceMibsData?.direct || []).find(m => m.id === mibId);
        }
        return (this.currentProfileMibsData?.mibs || []).find(m => m.id === mibId);
    }

    mibAssignmentUrl(scope, entityId, mibId) {
        const segment = scope === 'device' ? 'devices' : 'profiles';
        return `/api/${segment}/${entityId}/mibs/${mibId}/assignment`;
    }

    summarizeMibFilters(assignment) {
        const a = assignment || {};
        const includeCount = (a.include_names?.length || 0) + (a.include_oids?.length || 0);
        const excludeCount = (a.exclude_names?.length || 0) + (a.exclude_oids?.length || 0);

        const parts = [];
        if (includeCount) parts.push(`${includeCount} include`);
        if (excludeCount) parts.push(`${excludeCount} exclude`);
        if (a.max_oids) parts.push(`max ${a.max_oids} OIDs`);
        return parts.join(', ');
    }

    // buildAssignmentPayload builds the complete assignment body, since PUT replaces it wholesale.
    buildAssignmentPayload(assignment, overrides = {}) {
        const a = assignment || {};
        const payload = {
            enabled: overrides.enabled !== undefined ? overrides.enabled : !!a.enabled
        };

        const includeNames = overrides.include_names !== undefined ? overrides.include_names : (a.include_names || []);
        const excludeNames = overrides.exclude_names !== undefined ? overrides.exclude_names : (a.exclude_names || []);
        const includeOids = overrides.include_oids !== undefined ? overrides.include_oids : (a.include_oids || []);
        const excludeOids = overrides.exclude_oids !== undefined ? overrides.exclude_oids : (a.exclude_oids || []);
        const maxOids = overrides.max_oids !== undefined ? overrides.max_oids : (a.max_oids || 0);

        if (includeNames.length) payload.include_names = includeNames;
        if (excludeNames.length) payload.exclude_names = excludeNames;
        if (includeOids.length) payload.include_oids = includeOids;
        if (excludeOids.length) payload.exclude_oids = excludeOids;
        if (maxOids) payload.max_oids = maxOids;

        return payload;
    }

    async saveMibAssignment(scope, entityId, mib, overrides = {}) {
        const payload = this.buildAssignmentPayload(mib.assignment, overrides);

        const response = await fetch(this.mibAssignmentUrl(scope, entityId, mib.id), {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(payload)
        });

        if (!response.ok) {
            const text = await response.text().catch(() => '');
            throw new Error(`HTTP ${response.status}${text ? `: ${text}` : ''}`);
        }

        return await response.json();
    }

    async reloadMibList(scope, entityId) {
        if (scope === 'device') {
            await this.loadDeviceMIBs(entityId);
        } else {
            await this.loadProfileMIBs(entityId);
        }
    }

    // toggleDirectMib toggles a direct MIB; enabling on a device needs confirmation (confirmEnableMib).
    async toggleDirectMib(scope, entityId, mibId, checkboxEl) {
        const mib = this.findMibRecord(scope, mibId);
        if (!mib) {
            this.showNotification('MIB assignment not found, reloading…', 'error');
            await this.reloadMibList(scope, entityId);
            return;
        }

        const turningOn = checkboxEl.checked;
        checkboxEl.disabled = true;

        try {
            if (turningOn && scope === 'device') {
                const proceed = await this.confirmEnableMib(entityId, mib);
                if (!proceed) {
                    checkboxEl.checked = false;
                    checkboxEl.disabled = false;
                    return;
                }
            }

            await this.saveMibAssignment(scope, entityId, mib, { enabled: turningOn });
            this.showNotification(`${mib.name} ${turningOn ? 'enabled' : 'disabled'}`, 'success');
            await this.reloadMibList(scope, entityId);
        } catch (error) {
            console.error('Error updating MIB assignment:', error);
            this.showNotification(`Failed to ${turningOn ? 'enable' : 'disable'} ${mib.name}`, 'error');
            checkboxEl.checked = !turningOn;
            checkboxEl.disabled = false;
        }
    }

    // confirmEnableMib shows the estimated series impact in #modalOverlay and
    // resolves true/false depending on how the user responds. It never uses
    // window.confirm, since a native dialog blocks browser automation.
    confirmEnableMib(deviceId, mib) {
        return new Promise((resolve) => {
            const modalOverlay = document.getElementById('modalOverlay');
            const title = document.getElementById('modalOverlayTitle');
            const body = document.getElementById('modalOverlayBody');
            const closeBtn = modalOverlay.querySelector('.close-modal');

            title.textContent = `Enable ${mib.name}?`;
            body.innerHTML = '<p class="muted">Estimating impact…</p>';
            modalOverlay.style.display = 'flex';

            let settled = false;
            const finish = (result) => {
                if (settled) return;
                settled = true;
                closeBtn.removeEventListener('click', onOverlayClose);
                modalOverlay.style.display = 'none';
                resolve(result);
            };
            const onOverlayClose = () => finish(false);
            closeBtn.addEventListener('click', onOverlayClose);

            fetch(`/api/devices/${deviceId}/mibs/${mib.id}/estimate`)
                .then(response => {
                    if (!response.ok) throw new Error(`HTTP ${response.status}`);
                    return response.json();
                })
                .then(estimate => this.renderEnableConfirmation(body, mib, estimate, null, finish))
                .catch(error => this.renderEnableConfirmation(body, mib, null, error, finish));
        });
    }

    renderEnableConfirmation(body, mib, estimate, error, finish) {
        const highImpact = !!estimate && typeof estimate.series === 'number' && estimate.series > 5000;
        let html = '';

        if (estimate) {
            const approx = estimate.exact ? '' : '~';
            html += `<p>${this.escapeHtml(mib.name)}: ${approx}${estimate.series} series (estimated from ${estimate.instances} instances)</p>`;
            if (estimate.explanation) {
                html += `<p class="muted">${this.escapeHtml(estimate.explanation)}</p>`;
            }
            if (highImpact) {
                html += `
                    <div class="form-group">
                        <label style="flex-direction: row; align-items: center; gap: var(--s-2);">
                            <input type="checkbox" id="highImpactAck">
                            <span>This adds more than 5000 series. I understand and want to enable it anyway.</span>
                        </label>
                    </div>
                `;
            }
        } else {
            html += `<p class="muted">Could not estimate impact${error ? `: ${this.escapeHtml(error.message || String(error))}` : ''}. You can still enable this MIB.</p>`;
        }

        html += `
            <div class="modal-actions">
                <button type="button" class="secondary-btn" id="mibEnableCancelBtn">Cancel</button>
                <button type="button" class="primary-btn" id="mibEnableConfirmBtn" ${highImpact ? 'disabled' : ''}>Enable</button>
            </div>
        `;
        body.innerHTML = html;

        document.getElementById('mibEnableCancelBtn').addEventListener('click', () => finish(false));
        document.getElementById('mibEnableConfirmBtn').addEventListener('click', () => finish(true));

        if (highImpact) {
            const ack = document.getElementById('highImpactAck');
            const confirmBtn = document.getElementById('mibEnableConfirmBtn');
            ack.addEventListener('change', () => { confirmBtn.disabled = !ack.checked; });
        }
    }

    // openMibFilterModal edits include/exclude names, include/exclude OIDs
    // and max-oids for a single MIB assignment in #modalOverlay.
    openMibFilterModal(scope, entityId, mibId) {
        const mib = this.findMibRecord(scope, mibId);
        if (!mib) {
            this.showNotification('MIB assignment not found, reloading…', 'error');
            this.reloadMibList(scope, entityId);
            return;
        }

        const a = mib.assignment || {};
        const modalOverlay = document.getElementById('modalOverlay');
        const title = document.getElementById('modalOverlayTitle');
        const body = document.getElementById('modalOverlayBody');

        const field = (id, label, values) => `
            <div class="form-group">
                <span>${label}</span>
                <textarea id="${id}" rows="3" placeholder="One pattern per line">${this.escapeHtml((values || []).join('\n'))}</textarea>
                <span class="field-error" id="${id}Error"></span>
            </div>
        `;

        title.textContent = `Filters: ${mib.name}`;
        body.innerHTML = `
            <p class="muted">Patterns are regular expressions, one per line. An empty field matches everything.</p>
            ${field('mibFilterIncludeNames', 'Include names', a.include_names)}
            ${field('mibFilterExcludeNames', 'Exclude names', a.exclude_names)}
            ${field('mibFilterIncludeOids', 'Include OIDs', a.include_oids)}
            ${field('mibFilterExcludeOids', 'Exclude OIDs', a.exclude_oids)}
            <div class="form-group">
                <span>Max OIDs (blank or 0 = no limit)</span>
                <input type="number" id="mibFilterMaxOids" min="0" value="${a.max_oids || ''}">
                <span class="field-error" id="mibFilterMaxOidsError"></span>
            </div>
            <div class="modal-actions" style="justify-content: space-between;">
                <button type="button" class="secondary-btn" id="mibFilterClearBtn">Clear all filters</button>
                <div class="actions">
                    <button type="button" class="secondary-btn" id="mibFilterCancelBtn">Cancel</button>
                    <button type="button" class="primary-btn" id="mibFilterSaveBtn">Save filters</button>
                </div>
            </div>
        `;

        modalOverlay.style.display = 'flex';

        document.getElementById('mibFilterClearBtn').addEventListener('click', () => {
            ['mibFilterIncludeNames', 'mibFilterExcludeNames', 'mibFilterIncludeOids', 'mibFilterExcludeOids'].forEach(id => {
                document.getElementById(id).value = '';
            });
            document.getElementById('mibFilterMaxOids').value = '';
            this.clearMibFilterErrors();
        });
        document.getElementById('mibFilterCancelBtn').addEventListener('click', () => {
            modalOverlay.style.display = 'none';
        });
        document.getElementById('mibFilterSaveBtn').addEventListener('click', () => {
            this.saveMibFilters(scope, entityId, mib);
        });
    }

    clearMibFilterErrors() {
        document.querySelectorAll('#modalOverlayBody .field-error').forEach(el => {
            el.textContent = '';
            el.classList.remove('visible');
        });
        document.querySelectorAll('#modalOverlayBody .input-error').forEach(el => {
            el.classList.remove('input-error');
        });
    }

    // validateFilterField parses one regular expression per line, never comma-split; returns the lines or null with an inline error.
    validateFilterField(id) {
        const el = document.getElementById(id);
        const errorEl = document.getElementById(`${id}Error`);
        const lines = el.value.split('\n').map(l => l.trim()).filter(l => l.length > 0);

        for (let i = 0; i < lines.length; i++) {
            try {
                new RegExp(lines[i]);
            } catch (error) {
                el.classList.add('input-error');
                if (errorEl) {
                    errorEl.textContent = `Line ${i + 1} is not a valid pattern: "${lines[i]}" (${error.message})`;
                    errorEl.classList.add('visible');
                }
                return null;
            }
        }

        el.classList.remove('input-error');
        if (errorEl) {
            errorEl.textContent = '';
            errorEl.classList.remove('visible');
        }
        return lines;
    }

    async saveMibFilters(scope, entityId, mib) {
        const fieldIds = {
            include_names: 'mibFilterIncludeNames',
            exclude_names: 'mibFilterExcludeNames',
            include_oids: 'mibFilterIncludeOids',
            exclude_oids: 'mibFilterExcludeOids'
        };

        const overrides = {};
        let hasError = false;
        for (const [key, id] of Object.entries(fieldIds)) {
            const lines = this.validateFilterField(id);
            if (lines === null) {
                hasError = true;
            } else {
                overrides[key] = lines;
            }
        }

        const maxOidsInput = document.getElementById('mibFilterMaxOids');
        const maxOidsError = document.getElementById('mibFilterMaxOidsError');
        const maxOidsRaw = maxOidsInput.value.trim();
        let maxOids = 0;
        if (maxOidsRaw !== '') {
            maxOids = parseInt(maxOidsRaw, 10);
            if (Number.isNaN(maxOids) || maxOids < 0) {
                maxOidsInput.classList.add('input-error');
                maxOidsError.textContent = 'Max OIDs must be a non-negative whole number';
                maxOidsError.classList.add('visible');
                hasError = true;
            }
        }
        if (!hasError) {
            maxOidsInput.classList.remove('input-error');
            maxOidsError.textContent = '';
            maxOidsError.classList.remove('visible');
            overrides.max_oids = maxOids;
        }

        if (hasError) {
            this.showNotification('Fix the invalid pattern(s) before saving', 'error');
            return;
        }

        try {
            await this.saveMibAssignment(scope, entityId, mib, overrides);
            this.showNotification(`Filters saved for ${mib.name}`, 'success');
            document.getElementById('modalOverlay').style.display = 'none';
            await this.reloadMibList(scope, entityId);
        } catch (error) {
            console.error('Error saving MIB filters:', error);
            this.showNotification(`Failed to save filters for ${mib.name}`, 'error');
        }
    }

    // Per-object polling picker: shows the server's verdict per object; ticking edits the module's filters and re-fetches the plan.

    // The reason vocabulary is the CLI's (`netmon device mib objects`), so an
    // operator reading one recognises the other.
    objectPickerUrl(deviceId, mibId) {
        return `/api/devices/${encodeURIComponent(deviceId)}/mibs/${encodeURIComponent(mibId)}/objects`;
    }

    // assignmentDraft copies an assignment into the shape this modal edits.
    // Every field is present and owned by the draft, because the PUT replaces
    // the stored assignment wholesale.
    assignmentDraft(assignment) {
        const a = assignment || {};
        return {
            enabled: !!a.enabled,
            include_names: [...(a.include_names || [])],
            exclude_names: [...(a.exclude_names || [])],
            include_oids: [...(a.include_oids || [])],
            exclude_oids: [...(a.exclude_oids || [])],
            max_oids: a.max_oids || 0
        };
    }

    // patternMatches applies one stored pattern the way the server does: a
    // pattern that does not compile is not applied rather than fatal.
    patternMatches(pattern, value) {
        try {
            return new RegExp(pattern).test(value);
        } catch (error) {
            return false;
        }
    }

    // anchoredPattern turns a literal name or OID into a pattern that matches
    // exactly it, so ticking one object never silently catches its siblings.
    anchoredPattern(value) {
        return `^${String(value).replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}$`;
    }

    // findAppliedMib looks up a MIB the device page has already listed, direct
    // or profile-supplied, so the picker can be opened with ids alone.
    findAppliedMib(mibId) {
        mibId = parseInt(mibId, 10);
        const data = this.currentDeviceMibsData || {};
        return (data.direct || []).find(m => m.id === mibId)
            || (data.all || []).find(m => m.id === mibId)
            || null;
    }

    async openMibObjectPicker(deviceId, mibId) {
        const modalOverlay = document.getElementById('modalOverlay');
        const title = document.getElementById('modalOverlayTitle');
        const body = document.getElementById('modalOverlayBody');
        const known = this.findAppliedMib(mibId);
        const mibName = known ? known.name : `MIB ${mibId}`;

        title.textContent = `Objects: ${mibName}`;
        body.innerHTML = '<div class="mib-objects-picker"><p class="muted">Loading object plan…</p></div>';
        modalOverlay.style.display = 'flex';

        this.objectPicker = {
            deviceId: String(deviceId),
            mibId: parseInt(mibId, 10),
            mibName: mibName,
            plan: null,
            index: new Map(),
            draft: null,
            // oid -> the state the user asked for, when it differs from what
            // the server last reported.
            intents: new Map(),
            advancedEdited: false,
            shadowConfirmed: false,
            search: '',
            // Open on the rows that can be operated.
            show: 'actionable',
            sort: 'poller',
            // Pager position; edits live in the draft, so paging cannot lose them. Pages at LIST_PAGE.
            page: 0,
            size: this.pagerSize('objectPicker', LIST_PAGE),
            // Top-level rows, with each table's columns hanging off it. Built
            // from the plan; never a reordering of plan.objects itself.
            groups: [],
            // Table OIDs whose columns are open. null until the first plan
            // seeds it, then owned by the user across re-fetches.
            expanded: null,
            // Kept across the re-fetch a save triggers: re-reading the plan
            // must not throw the user back to the top of a 293-row list with
            // the Advanced section shut again.
            advancedOpen: false,
            scrollTop: 0,
            saving: false
        };

        await this.loadMibObjectPlan();
    }

    async loadMibObjectPlan() {
        const state = this.objectPicker;
        if (!state) return;

        try {
            const response = await fetch(this.objectPickerUrl(state.deviceId, state.mibId));
            if (!response.ok) {
                const text = await response.text().catch(() => '');
                throw new Error(text.trim() || `HTTP ${response.status}`);
            }
            const plan = await response.json();
            if (this.objectPicker !== state) return; // the modal moved on

            state.plan = plan;
            state.mibName = plan.mib || state.mibName;
            document.getElementById('modalOverlayTitle').textContent = `Objects: ${state.mibName}`;
            state.index = new Map((plan.objects || []).map(obj => [obj.oid, obj]));
            this.buildObjectGroups();
            state.draft = this.assignmentDraft(plan.assignment);
            state.intents.clear();
            state.advancedEdited = false;
            state.shadowConfirmed = false;
            this.renderObjectPicker();
        } catch (error) {
            console.error('Error loading object plan:', error);
            const body = document.getElementById('modalOverlayBody');
            body.innerHTML = `
                <div class="mib-objects-picker">
                    <p class="mib-accordion-error">Could not load the object plan: ${this.escapeHtml(error.message || String(error))}</p>
                    <div class="modal-actions">
                        <button type="button" class="secondary-btn" id="objPickerCancelBtn">Close</button>
                    </div>
                </div>
            `;
            const close = document.getElementById('objPickerCancelBtn');
            if (close) close.addEventListener('click', () => this.closeObjectPicker());
        }
    }

    closeObjectPicker() {
        this.objectPicker = null;
        document.getElementById('modalOverlay').style.display = 'none';
    }

    // objectTableFor finds the table covering an object: table_oid, or the longest prefixing table OID.
    objectTableFor(obj) {
        const state = this.objectPicker;
        if (obj.table_oid) {
            const direct = state.index.get(obj.table_oid);
            if (direct) return direct;
        }
        let best = null;
        for (const candidate of state.plan.objects || []) {
            if (candidate.kind !== 'table') continue;
            if (!obj.oid.startsWith(`${candidate.oid}.`)) continue;
            if (!best || candidate.oid.length > best.oid.length) best = candidate;
        }
        return best;
    }

    // objectParent is the table a column or entry structurally belongs to.
    objectParent(obj) {
        if (obj.kind !== 'column' && obj.kind !== 'entry') return null;
        const table = this.objectTableFor(obj);
        return table && table.oid !== obj.oid ? table : null;
    }

    // buildObjectGroups groups the plan into scalars and tables with their columns, keeping the server's order.
    buildObjectGroups() {
        const state = this.objectPicker;
        const objects = state.plan.objects || [];

        const children = new Map();
        const top = [];
        for (const obj of objects) {
            const table = this.objectParent(obj);
            if (!table) {
                top.push(obj);
                continue;
            }
            if (!children.has(table.oid)) children.set(table.oid, []);
            children.get(table.oid).push(obj);
        }

        state.groups = top.map(obj => ({ obj, children: children.get(obj.oid) || [] }));

        // Columns stay folded unless one is separately operable.
        if (state.expanded === null) state.expanded = new Set();
        for (const group of state.groups) {
            if (group.children.some(child => this.objectControl(child) === 'toggle')) {
                state.expanded.add(group.obj.oid);
            }
        }
    }

    // objectChecked reports the state to paint: the user's pending intent if
    // there is one, otherwise the server's verdict, and never anything
    // computed from the patterns.
    objectChecked(obj) {
        const intent = this.objectPicker.intents.get(obj.oid);
        return intent === undefined ? !!obj.polled : intent;
    }

    // objectControl maps the server's verdict to a control: toggle, via-table, never or paused; unknown reasons get a toggle.
    objectControl(obj) {
        if (obj.polled) return 'toggle';
        switch (obj.reason) {
            case 'covered-by-table-walk': return 'via-table';
            case 'not-accessible':
            case 'not-current': return 'never';
            case 'module-disabled': return 'paused';
            default: return 'toggle';
        }
    }

    // ---- verdicts -------------------------------------------------------

    // matchingPatterns lists the draft patterns that select a value, used to
    // name the exact rule an operator has to undo.
    matchingPatterns(patterns, value) {
        return (patterns || []).filter(pattern => this.patternMatches(pattern, value));
    }

    // patternReach counts how many of the module's objects a pattern matches,
    // so "remove this exclude" can admit when it will free more than one.
    patternReach(pattern, field) {
        const objects = this.objectPicker.plan.objects || [];
        const oidField = field === 'oid';
        let count = 0;
        for (const obj of objects) {
            if (this.patternMatches(pattern, oidField ? obj.oid : obj.name)) count++;
        }
        return count;
    }

    // objectVerdictHtml explains a row's state; covered-by-table-walk names the table's control.
    objectVerdictHtml(obj, grouped) {
        if (obj.polled || obj.reason === 'polled') return '';
        if (obj.reason === 'module-disabled') return '';

        switch (obj.reason) {
            case 'covered-by-table-walk': {
                const table = this.objectTableFor(obj);
                const tableName = table ? this.escapeHtml(table.name) : 'its table';
                // A table the user has just unticked stops returning its
                // columns the moment that is saved, and saying "Collected"
                // until then would be the old contradiction upside down.
                if (table && !this.objectChecked(table)) {
                    return `
                        <span class="tag-badge">will stop</span>
                        <span>Collected by the ${tableName} walk today; unticking ${tableName} stops this too, once saved.</span>
                    `;
                }
                if (grouped) {
                    return `<span class="badge enabled">Collected</span>
                        <span>by the ${tableName} walk above.</span>`;
                }
                const what = obj.kind === 'entry' ? 'every column of every row' : 'this column for every row';
                const action = table
                    ? `<button type="button" class="link-btn" data-exclude-table="${this.escapeAttr(table.oid)}">Stop collecting ${tableName}</button>`
                    : '';
                return `
                    <span class="badge enabled">Collected</span>
                    <span>Collected by the ${tableName} walk — one walk returns ${what}.
                    Excluding this ${this.escapeHtml(obj.kind)} on its own would not stop it.</span>
                    ${action}
                `;
            }
            case 'not-accessible': {
                const access = obj.access && obj.access !== 'unknown'
                    ? `MAX-ACCESS is ${this.escapeHtml(obj.access)}`
                    : 'this is a node in the OID tree rather than a readable object';
                return `
                    <span class="tag-badge">not-accessible</span>
                    <span>Not pollable: ${access}, so the agent can never return a value. No filter changes that.</span>
                `;
            }
            case 'not-current':
                return `
                    <span class="tag-badge">not-current</span>
                    <span>Not pollable: the MIB marks this object ${this.escapeHtml(obj.status || 'deprecated')}, and the poller
                    does not spend a varbind on one. No filter changes that.</span>
                `;
            case 'excluded-by-filter': {
                const names = this.matchingPatterns(this.objectPicker.draft.exclude_names, obj.name);
                const oids = this.matchingPatterns(this.objectPicker.draft.exclude_oids, obj.oid);
                const all = [
                    ...names.map(p => ({ pattern: p, field: 'name' })),
                    ...oids.map(p => ({ pattern: p, field: 'oid' }))
                ];
                const listed = all.map(({ pattern, field }) => {
                    const reach = this.patternReach(pattern, field);
                    const also = reach > 1 ? ` <span class="muted">(also matches ${reach - 1} other object${reach === 2 ? '' : 's'})</span>` : '';
                    return `<code class="mono">${this.escapeHtml(pattern)}</code>${also}`;
                }).join(', ');
                return `
                    <span class="tag-badge">excluded-by-filter</span>
                    <span>Removed by ${listed || 'an exclude pattern'}. Ticking the box deletes
                    ${all.length > 1 ? 'those patterns' : 'that pattern'}.</span>
                `;
            }
            case 'not-included-by-filter':
                return `
                    <span class="tag-badge">not-included-by-filter</span>
                    <span>The module has include patterns and none of them matches this object.
                    Ticking the box adds <code class="mono">${this.escapeHtml(this.anchoredPattern(obj.name))}</code>.</span>
                `;
            case 'capped-by-max-oids':
                return `
                    <span class="tag-badge">capped-by-max-oids</span>
                    <span>The max-OIDs cap (${this.objectPicker.draft.max_oids || 0}) was already spent by the time the
                    poller reached this object. Ticking the box raises the cap.</span>
                `;
            default:
                return `<span class="tag-badge">${this.escapeHtml(obj.reason || 'not polled')}</span>`;
        }
    }

    // ---- selection edits -------------------------------------------------

    // maxOidsNeededFor counts the polled or capped objects at or before this one in server order.
    maxOidsNeededFor(target) {
        let needed = 0;
        for (const obj of this.objectPicker.plan.objects || []) {
            if (obj.polled || obj.reason === 'capped-by-max-oids') needed++;
            if (obj.oid === target.oid) break;
        }
        return needed;
    }

    // selectObject edits the draft so the module's rules stop rejecting one
    // object: drop the excludes that catch it, make sure an include list (if
    // there is one) admits it, and lift a cap that was spent before it.
    selectObject(obj) {
        const draft = this.objectPicker.draft;

        draft.exclude_names = draft.exclude_names.filter(p => !this.patternMatches(p, obj.name));
        draft.exclude_oids = draft.exclude_oids.filter(p => !this.patternMatches(p, obj.oid));

        if (draft.include_names.length && !draft.include_names.some(p => this.patternMatches(p, obj.name))) {
            draft.include_names.push(this.anchoredPattern(obj.name));
        }
        if (draft.include_oids.length && !draft.include_oids.some(p => this.patternMatches(p, obj.oid))) {
            draft.include_oids.push(this.anchoredPattern(obj.oid));
        }
        if (draft.max_oids && obj.reason === 'capped-by-max-oids') {
            draft.max_oids = Math.max(draft.max_oids, this.maxOidsNeededFor(obj));
        }
    }

    // deselectObject adds one anchored exclude. Excludes are evaluated before
    // includes, so this is enough whatever the include patterns say.
    deselectObject(obj) {
        const draft = this.objectPicker.draft;
        if (this.matchingPatterns(draft.exclude_names, obj.name).length) return;
        if (this.matchingPatterns(draft.exclude_oids, obj.oid).length) return;
        draft.exclude_names.push(this.anchoredPattern(obj.name));
    }

    toggleObjectSelection(oid, checked) {
        const state = this.objectPicker;
        const obj = state ? state.index.get(oid) : null;
        if (!obj) return;

        if (checked) {
            this.selectObject(obj);
        } else {
            this.deselectObject(obj);
        }

        if (checked === !!obj.polled) {
            state.intents.delete(oid);
        } else {
            state.intents.set(oid, checked);
        }

        // A table governs its columns, so toggling one changes what those rows
        // say about themselves and the block is repainted.
        const group = state.groups.find(g => g.obj.oid === oid);
        if (group && group.children.length) {
            this.repaintObjectPickerList();
            return;
        }

        const row = document.querySelector(`#objPickerList .obj-row[data-oid="${CSS.escape(oid)}"]`);
        if (row) {
            row.classList.toggle('is-pending', state.intents.has(oid));
            row.classList.toggle('is-selected', checked);
        }
        this.afterObjectPickerEdit();
    }

    // repaintObjectPickerList re-renders the rows without throwing the reader
    // back to the top of the list.
    repaintObjectPickerList() {
        const list = document.getElementById('objPickerList');
        const top = list ? list.scrollTop : 0;
        this.refreshObjectPickerList();
        const after = document.getElementById('objPickerList');
        if (after) after.scrollTop = top;
    }

    // toggleObjectGroup opens or closes one table's columns. It touches the DOM
    // it already rendered rather than repainting the list, so the reader keeps
    // both their place and their keyboard focus.
    toggleObjectGroup(tableOid) {
        const state = this.objectPicker;
        if (!state) return;

        const open = !state.expanded.has(tableOid);
        if (open) state.expanded.add(tableOid); else state.expanded.delete(tableOid);

        const host = document.querySelector(`#objPickerList .obj-group[data-group-oid="${CSS.escape(tableOid)}"]`);
        const group = (state.visible || []).find(candidate => candidate.obj.oid === tableOid);
        if (!host || !group) return;

        const children = host.querySelector('.obj-children');
        if (children) children.hidden = !open;
        const note = host.querySelector('.obj-group-note');
        if (note) {
            note.innerHTML = this.renderObjectGroupNote(group, open);
            const button = note.querySelector('.obj-cols-toggle');
            if (button) button.focus();
        }
    }

    setObjectPickerShow(value) {
        const state = this.objectPicker;
        if (!state) return;
        state.show = value;
        state.page = 0;
        const select = document.getElementById('objPickerShow');
        if (select) select.value = value;
        this.refreshObjectPickerList();
    }

    excludeTableForObject(tableOid) {
        const state = this.objectPicker;
        const table = state ? state.index.get(tableOid) : null;
        if (!table) return;

        const selector = `#objPickerList .obj-row[data-oid="${CSS.escape(tableOid)}"] input[type="checkbox"]`;
        const box = document.querySelector(selector);
        if (box) box.checked = false;
        this.toggleObjectSelection(tableOid, false);

        // The table's block repaints when it changes, so focus lands on the
        // checkbox that exists now rather than on the one that was replaced.
        const current = document.querySelector(selector);
        if (current) {
            current.focus();
        } else {
            this.showNotification(`${table.name} excluded — it is outside the current search`, 'success');
        }
    }

    // ---- totals and drift ------------------------------------------------

    // objectPickerTotals prices the current selection from the per-object
    // `series` the server sent. An object being newly selected has no measured
    // cost yet, which is said out loud rather than guessed at.
    objectPickerTotals() {
        const state = this.objectPicker;
        const objects = state.plan.objects || [];

        let selected = 0;
        let series = 0;
        let unpriced = 0;
        // How many rows are controls at all, and how many objects ride along on
        // a table walk — the two numbers that explain why "24 of 293" is not a
        // sign that something is broken.
        let configurable = 0;
        let viaTable = 0;
        let never = 0;
        for (const obj of objects) {
            const control = this.objectControl(obj);
            if (control === 'toggle') configurable++;
            if (control === 'via-table') viaTable++;
            if (control === 'never') never++;

            if (!this.objectChecked(obj)) continue;
            selected++;
            if (obj.polled) {
                series += obj.series || 0;
            } else {
                unpriced++;
            }
        }

        const estimate = state.plan.estimate || {};
        return {
            selected,
            configurable,
            viaTable,
            never,
            total: state.plan.total || objects.length,
            series,
            unpriced,
            // Nothing selected costs exactly nothing, so "~0" would be a lie
            // in the other direction.
            approximate: (estimate.exact === false || unpriced > 0) && (series > 0 || unpriced > 0)
        };
    }

    // objectPickerChanges lists every difference between the draft and the
    // assignment the plan was computed from, which is exactly what a Save
    // would write.
    objectPickerChanges() {
        const state = this.objectPicker;
        const before = this.assignmentDraft(state.plan.assignment);
        const after = state.draft;
        const changes = [];

        if (before.enabled !== after.enabled) {
            changes.push(after.enabled ? 'start polling this module' : 'pause polling for this module');
        }
        const fields = [
            ['include_names', 'include-names'],
            ['exclude_names', 'exclude-names'],
            ['include_oids', 'include-OIDs'],
            ['exclude_oids', 'exclude-OIDs']
        ];
        for (const [key, label] of fields) {
            const added = after[key].filter(p => !before[key].includes(p));
            const removed = before[key].filter(p => !after[key].includes(p));
            if (added.length) changes.push(`add ${added.length} ${label} pattern${added.length === 1 ? '' : 's'}`);
            if (removed.length) changes.push(`remove ${removed.length} ${label} pattern${removed.length === 1 ? '' : 's'}`);
        }
        if (before.max_oids !== after.max_oids) {
            changes.push(`set max OIDs to ${after.max_oids || 'no limit'}`);
        }
        return changes;
    }

    // ---- rendering -------------------------------------------------------

    // objectMatchesSearch is the one text filter, applied to top-level rows and
    // to columns alike so a column is still findable by name inside its table.
    objectMatchesSearch(obj, term) {
        if (!term) return true;
        return (obj.name || '').toLowerCase().includes(term)
            || (obj.oid || '').includes(term)
            || (obj.description || '').toLowerCase().includes(term);
    }

    // showsObject applies the Show filter to a top-level row. Columns are not
    // subject to it: they are detail of their table, not siblings of it.
    showsObject(obj) {
        switch (this.objectPicker.show) {
            case 'polled': return !!obj.polled;
            case 'unpolled': return !obj.polled;
            case 'all': return true;
            default: return this.objectControl(obj) === 'toggle';
        }
    }

    // visibleGroups applies search and display order to a copy and reports what the Show filter hides.
    visibleGroups() {
        const state = this.objectPicker;
        const term = state.search.trim().toLowerCase();

        const groups = [];
        const hidden = [];
        for (const group of state.groups) {
            const parentHit = this.objectMatchesSearch(group.obj, term);
            const children = term
                ? group.children.filter(child => this.objectMatchesSearch(child, term))
                : group.children;
            if (term && !parentHit && children.length === 0) continue;

            if (!this.showsObject(group.obj)) {
                hidden.push(group.obj);
                continue;
            }
            groups.push({
                obj: group.obj,
                children,
                // A search that only hit columns opens the table it hit, so
                // the match is on screen rather than behind a disclosure.
                forced: !!term && !parentHit && children.length > 0
            });
        }

        const ordered = state.sort === 'name'
            ? [...groups].sort((a, b) => (a.obj.name || '').localeCompare(b.obj.name || ''))
            : groups;
        return { groups: ordered, hidden };
    }

    // hiddenSummary names what the Show filter is holding back, by reason, so
    // the offer to reveal it says what will appear.
    hiddenSummary(hidden) {
        const counts = new Map();
        for (const obj of hidden) {
            const label = obj.reason === 'not-current'
                ? 'deprecated'
                : (obj.reason === 'not-accessible' ? 'not readable by SNMP' : (obj.reason || 'other'));
            counts.set(label, (counts.get(label) || 0) + 1);
        }
        return [...counts.entries()]
            .sort((a, b) => b[1] - a[1])
            .map(([label, count]) => `${count.toLocaleString()} ${label}`)
            .join(', ');
    }

    // groupExpanded decides whether a table's columns are on screen: the
    // user's own choice, or a search that landed inside the table.
    groupExpanded(group) {
        return group.forced || this.objectPicker.expanded.has(group.obj.oid);
    }

    renderObjectPicker() {
        const state = this.objectPicker;
        const plan = state.plan;
        const body = document.getElementById('modalOverlayBody');
        const estimate = plan.estimate || {};
        const estimateText = typeof estimate.series === 'number'
            ? `${estimate.exact === false ? '~' : ''}${estimate.series.toLocaleString()} series`
            : 'not estimated';

        body.innerHTML = `
            <div class="mib-objects-picker">
                <div class="obj-picker-summary">
                    <div>
                        <strong>${this.escapeHtml(plan.mib)}</strong>
                        <span class="muted">on ${this.escapeHtml(plan.device)}</span>
                    </div>
                    <div class="obj-picker-summary-meta">
                        <span class="count-pill">${(plan.polled || 0).toLocaleString()} of ${(plan.total || 0).toLocaleString()} polled</span>
                        <span class="count-pill">module ${estimateText}</span>
                        <span class="tag-badge">from ${this.escapeHtml(plan.source || 'default')}</span>
                    </div>
                </div>
                ${estimate.explanation ? `<p class="obj-picker-note muted">${this.escapeHtml(estimate.explanation)}</p>` : ''}

                <label class="obj-picker-module">
                    <span class="switch">
                        <input type="checkbox" class="switch-input" id="objPickerEnabled" ${state.draft.enabled ? 'checked' : ''}
                            aria-label="Poll ${this.escapeAttr(plan.mib)} on ${this.escapeAttr(plan.device)}">
                        <span class="switch-track" aria-hidden="true"></span>
                    </span>
                    <span>Poll this module on ${this.escapeHtml(plan.device)}</span>
                </label>

                <div id="objPickerBanners"></div>

                <div class="obj-picker-controls">
                    <input type="search" id="objPickerSearch" placeholder="Search name, OID or description"
                        aria-label="Search objects" value="${this.escapeAttr(state.search)}">
                    <label class="obj-picker-select">
                        <span>Show</span>
                        <select id="objPickerShow" aria-label="Filter objects by polling state">
                            <option value="actionable" ${state.show === 'actionable' ? 'selected' : ''}>Configurable objects</option>
                            <option value="polled" ${state.show === 'polled' ? 'selected' : ''}>Polled only</option>
                            <option value="unpolled" ${state.show === 'unpolled' ? 'selected' : ''}>Not polled</option>
                            <option value="all" ${state.show === 'all' ? 'selected' : ''}>All objects</option>
                        </select>
                    </label>
                    <label class="obj-picker-select">
                        <span>Order</span>
                        <select id="objPickerSort" aria-label="Object display order">
                            <option value="poller" ${state.sort === 'poller' ? 'selected' : ''}>Poller order</option>
                            <option value="name" ${state.sort === 'name' ? 'selected' : ''}>Name</option>
                        </select>
                    </label>
                    ${state.groups.length > LIST_PAGE
                        ? this.pagerSizeHtml('objPickerSize', LIST_PAGE, state.size, 'objects')
                        : ''}
                </div>

                <div class="obj-picker-head" aria-hidden="true">
                    <span>Collected / Name</span><span>OID</span><span>Type</span><span>Kind</span><span>Access</span><span class="obj-series">Series</span>
                </div>
                <div class="obj-picker-list" id="objPickerList" role="group" aria-label="Objects of ${this.escapeAttr(plan.mib)}"></div>
                <div class="paged-list-footer" id="objPickerFooter">
                    <nav class="pager-nav" id="objPickerPager"
                         aria-label="${this.escapeAttr(plan.mib)} objects pagination" hidden></nav>
                    <p class="pager-status" id="objPickerStatus" role="status" aria-live="polite"></p>
                    <div id="objPickerReveal"></div>
                </div>
                <p class="obj-picker-total" id="objPickerTotal" role="status" aria-live="polite"></p>

                <details class="obj-picker-advanced" id="objPickerAdvanced" ${state.advancedOpen ? 'open' : ''}>
                    <summary>Advanced — raw filter patterns</summary>
                    <p class="muted">Patterns are regular expressions, one per line. An empty field matches everything.
                    Excludes are applied before includes.</p>
                    ${this.renderObjectPickerPatternField('objPickerIncludeNames', 'Include names')}
                    ${this.renderObjectPickerPatternField('objPickerExcludeNames', 'Exclude names')}
                    ${this.renderObjectPickerPatternField('objPickerIncludeOids', 'Include OIDs')}
                    ${this.renderObjectPickerPatternField('objPickerExcludeOids', 'Exclude OIDs')}
                    <div class="form-group">
                        <span>Max OIDs (blank or 0 = no limit)</span>
                        <input type="number" id="objPickerMaxOids" min="0">
                        <span class="field-error" id="objPickerMaxOidsError"></span>
                    </div>
                </details>

                <div id="objPickerConfirm"></div>
                <div class="modal-actions">
                    <button type="button" class="secondary-btn" id="objPickerCancelBtn">Cancel</button>
                    <button type="button" class="primary-btn" id="objPickerSaveBtn">Save</button>
                </div>
            </div>
        `;

        this.syncObjectPickerAdvanced();
        this.refreshObjectPickerList();
        this.bindObjectPicker();

        const list = document.getElementById('objPickerList');
        if (list && state.scrollTop) list.scrollTop = state.scrollTop;
    }

    renderObjectPickerPatternField(id, label) {
        return `
            <div class="form-group">
                <span>${label}</span>
                <textarea id="${id}" rows="2" placeholder="One pattern per line"></textarea>
                <span class="field-error" id="${id}Error"></span>
            </div>
        `;
    }

    // syncObjectPickerAdvanced writes the draft into the Advanced fields so
    // ticking a box visibly changes the patterns it edits. A field the user is
    // typing in is left alone.
    syncObjectPickerAdvanced() {
        const draft = this.objectPicker.draft;
        const fields = [
            ['objPickerIncludeNames', draft.include_names],
            ['objPickerExcludeNames', draft.exclude_names],
            ['objPickerIncludeOids', draft.include_oids],
            ['objPickerExcludeOids', draft.exclude_oids]
        ];
        for (const [id, values] of fields) {
            const el = document.getElementById(id);
            if (el && document.activeElement !== el) el.value = values.join('\n');
        }
        const maxOids = document.getElementById('objPickerMaxOids');
        if (maxOids && document.activeElement !== maxOids) {
            maxOids.value = draft.max_oids ? String(draft.max_oids) : '';
        }
    }

    refreshObjectPickerList() {
        const state = this.objectPicker;
        const list = document.getElementById('objPickerList');
        const footer = document.getElementById('objPickerFooter');
        if (!list || !footer) return;

        // Show filter and search narrow the set; the pager counts what is left.
        const { groups, hidden } = this.visibleGroups();
        state.visible = groups;
        const slice = this.pagerSlice(groups.length, state.page, state.size);
        state.page = slice.page;

        list.classList.toggle('is-module-off', !state.draft.enabled);
        list.innerHTML = groups.length === 0
            ? `<p class="empty-state">${hidden.length
                ? 'Nothing configurable matches — the objects that match are reference-only'
                : 'No object matches this search'}</p>`
            : this.renderObjectPickerGroups(groups, slice.from, slice.to);

        this.paintPager('objPickerPager', 'objPickerStatus', {
            slice,
            matched: groups.length,
            total: state.groups.length,
            noun: 'rows',
            label: `${state.plan.mib} objects`
        });
        const reveal = document.getElementById('objPickerReveal');
        if (reveal) reveal.innerHTML = this.objectPickerRevealHtml(hidden);
        this.afterObjectPickerEdit();
    }

    renderObjectPickerGroups(groups, from, to) {
        let html = '';
        for (let i = from; i < to; i++) html += this.renderObjectPickerGroup(groups[i]);
        return html;
    }

    // A table and its columns are one thing to configure, so they render as one
    // block: the table carries the checkbox, the columns are its detail.
    renderObjectPickerGroup(group) {
        const parent = this.renderObjectPickerRow(group.obj, null);
        if (group.children.length === 0) return `<div class="obj-group">${parent}</div>`;

        const open = this.groupExpanded(group);
        const count = group.children.length;
        const columns = group.children
            .map(child => this.renderObjectPickerRow(child, group.obj))
            .join('');

        return `
            <div class="obj-group" data-group-oid="${this.escapeAttr(group.obj.oid)}">
                ${parent}
                <div class="obj-group-note">${this.renderObjectGroupNote(group, open)}</div>
                <div class="obj-children" id="objCols-${this.escapeAttr(group.obj.oid)}" ${open ? '' : 'hidden'}>
                    ${columns}
                </div>
            </div>
        `;
    }

    // renderObjectGroupNote is the line between a table and its columns: the
    // disclosure, what is inside, and — once for the whole table rather than
    // once per column — why the table is the control.
    renderObjectGroupNote(group, open) {
        const count = group.children.length;
        const walked = group.children.filter(child => this.objectControl(child) === 'via-table').length;
        const detail = open && walked && this.objectChecked(group.obj)
            ? `<span>One walk returns all of them, so excluding a column on its own would not stop it.</span>
               <button type="button" class="link-btn" data-exclude-table="${this.escapeAttr(group.obj.oid)}">Stop collecting ${this.escapeHtml(group.obj.name)}</button>`
            : '';

        return `
            <button type="button" class="link-btn obj-cols-toggle"
                data-cols="${this.escapeAttr(group.obj.oid)}" aria-expanded="${open ? 'true' : 'false'}"
                aria-controls="objCols-${this.escapeAttr(group.obj.oid)}">
                ${open ? 'Hide' : 'Show'} ${count.toLocaleString()} column${count === 1 ? '' : 's'}
            </button>
            <span class="muted">${this.escapeHtml(this.groupChildSummary(group))}</span>
            ${detail}
        `;
    }

    // groupChildSummary counts a table's columns by the verdict the server gave
    // each of them, so the closed group still says what is inside it.
    groupChildSummary(group) {
        let via = 0;
        let own = 0;
        let never = 0;
        for (const child of group.children) {
            switch (this.objectControl(child)) {
                case 'via-table': via++; break;
                case 'never': never++; break;
                default: if (child.polled) own++; break;
            }
        }
        const parts = [];
        if (via) parts.push(`${via.toLocaleString()} collected by this walk`);
        if (own) parts.push(`${own.toLocaleString()} polled on its own`);
        if (never) parts.push(`${never.toLocaleString()} not pollable`);
        return parts.join(' · ');
    }

    // renderObjectPickerRow paints one object; only operable rows get a checkbox.
    renderObjectPickerRow(obj, table) {
        const state = this.objectPicker;
        const checked = this.objectChecked(obj);
        const pending = state.intents.has(obj.oid);
        const control = this.objectControl(obj);
        const verdict = state.draft.enabled ? this.objectVerdictHtml(obj, !!table) : '';

        const classes = ['obj-row', `is-${control}`];
        if (checked) classes.push('is-selected');
        if (pending) classes.push('is-pending');
        if (table) classes.push('is-child');

        // One clipped DESCRIPTION line, full text in the sheet.
        const description = this.descriptionTriggerHtml(obj.description, {
            title: obj.name,
            eyebrow: 'SNMP object',
            className: 'obj-row-desc',
            meta: [
                ['OID', obj.oid],
                ['Syntax', obj.type],
                ['Access', obj.access],
                ['Kind', obj.kind]
            ]
        });

        return `
            <div class="${classes.join(' ')}" data-oid="${this.escapeAttr(obj.oid)}">
                <div class="obj-row-main">
                    ${this.renderObjectPickerControl(obj, table, control, checked)}
                    <span class="obj-oid mono">${this.escapeHtml(obj.oid)}</span>
                    <span class="obj-type">${this.escapeHtml(obj.type || '—')}</span>
                    <span class="obj-kind"><span class="tag-badge">${this.escapeHtml(obj.kind || '')}</span></span>
                    <span class="obj-access">${this.escapeHtml(obj.access || '—')}</span>
                    <span class="obj-series">${(obj.series || 0).toLocaleString()}</span>
                </div>
                ${description}
                ${verdict ? `<div class="obj-verdict">${verdict}</div>` : ''}
            </div>
        `;
    }

    // renderObjectPickerControl is the first cell: a checkbox, or a read-only
    // marker whose state matches the verdict the row goes on to explain.
    renderObjectPickerControl(obj, table, control, checked) {
        const name = `<span class="obj-name">${this.escapeHtml(obj.name)}</span>`;

        if (control === 'toggle') {
            const label = checked ? `Stop polling ${obj.name}` : `Poll ${obj.name}`;
            return `
                <label class="obj-check">
                    <input type="checkbox" data-object-oid="${this.escapeAttr(obj.oid)}"
                        ${checked ? 'checked' : ''} aria-label="${this.escapeAttr(label)}">
                    ${name}
                </label>
            `;
        }

        // Collected via a walk the user has just unticked: still on today, off
        // as soon as the pending change is saved.
        const stopping = control === 'via-table' && table && !this.objectChecked(table);
        const marks = {
            'via-table': stopping
                ? { glyph: '✓', cls: 'is-stopping', label: `Collected by the ${table.name} walk, which is unticked and will stop when saved` }
                : { glyph: '✓', cls: 'is-on', label: `Collected by the ${table ? table.name : 'table'} walk` },
            never: { glyph: '—', cls: 'is-never', label: `${obj.name} cannot be polled` },
            paused: { glyph: '—', cls: 'is-never', label: `${obj.name} is not polled: the module is paused` }
        };
        const mark = marks[control] || marks.never;
        return `
            <span class="obj-check is-static">
                <span class="obj-state ${mark.cls}" role="img" aria-label="${this.escapeAttr(mark.label)}"
                    title="${this.escapeAttr(mark.label)}"><span aria-hidden="true">${mark.glyph}</span></span>
                ${name}
            </span>
        `;
    }

    // Paging is the shared pager above; this is the only part of the picker's
    // footer that is its own — the offer to reveal what the Show filter is
    // holding back, which the pager's counts cannot state on their own.
    objectPickerRevealHtml(hidden) {
        const state = this.objectPicker;

        if (hidden.length) {
            return `
                <p class="obj-picker-hidden">
                    <span class="muted">${hidden.length.toLocaleString()} reference-only object${hidden.length === 1 ? '' : 's'}
                    hidden (${this.escapeHtml(this.hiddenSummary(hidden))}) — nothing you can turn on or off.</span>
                    <button type="button" class="link-btn" id="objPickerShowAllBtn">Show all ${(state.plan.total || 0).toLocaleString()} objects</button>
                </p>
            `;
        }
        if (state.show !== 'actionable') {
            return `
                <p class="obj-picker-hidden">
                    <button type="button" class="link-btn" id="objPickerShowActionableBtn">Back to configurable objects only</button>
                </p>
            `;
        }
        return '';
    }

    // afterObjectPickerEdit repaints everything that depends on the draft:
    // the running cost, the honesty banners, and the Advanced fields.
    afterObjectPickerEdit() {
        const state = this.objectPicker;
        if (!state || !state.plan) return;

        const totals = this.objectPickerTotals();
        const totalEl = document.getElementById('objPickerTotal');
        if (totalEl) {
            const approx = totals.approximate ? '~' : '';
            let text = `Selected ${totals.selected.toLocaleString()} of ${totals.configurable.toLocaleString()} configurable objects`
                + ` → ${approx}${totals.series.toLocaleString()} series`;
            if (totals.unpriced > 0) {
                text += ` — ${totals.unpriced.toLocaleString()} newly selected object${totals.unpriced === 1 ? '' : 's'} `
                    + 'cost nothing measurable until it is saved and polled';
            }
            totalEl.textContent = text;
        }

        this.syncObjectPickerAdvanced();
        this.renderObjectPickerBanners();
    }

    // objectPickerHowToHtml states the picker's state and available actions.
    objectPickerHowToHtml() {
        const state = this.objectPicker;
        const totals = this.objectPickerTotals();
        const objects = state.plan.objects || [];

        // Counted from plan verdicts, not from the draft, so it describes what
        // is stored rather than what is pending.
        let polled = 0;
        let offByFilter = 0;
        for (const obj of objects) {
            if (this.objectControl(obj) !== 'toggle') continue;
            if (obj.polled) polled++; else offByFilter++;
        }

        const lead = offByFilter === 0
            ? `<strong>Everything ${this.escapeHtml(state.plan.mib)} can poll on ${this.escapeHtml(state.plan.device)} is being polled.</strong>
               All ${polled.toLocaleString()} configurable objects are on, so there is nothing left to turn on — untick one to stop
               collecting it, and it becomes tickable again the moment it is excluded.`
            : `<strong>${polled.toLocaleString()} of ${totals.configurable.toLocaleString()} configurable objects are polled.</strong>
               ${offByFilter === 1
                    ? 'Tick the excluded object to start collecting it again'
                    : `Tick any of the ${offByFilter.toLocaleString()} excluded objects to start collecting them again`},
               or untick a polled one to stop.`;

        const rest = [];
        if (totals.viaTable) {
            rest.push(`${totals.viaTable.toLocaleString()} table column${totals.viaTable === 1 ? '' : 's'} arrive with their table's walk
                and are listed under it — the table is the control for them`);
        }
        if (totals.never) {
            rest.push(`${totals.never.toLocaleString()} object${totals.never === 1 ? '' : 's'} can never be polled (deprecated, or not readable
                by SNMP) and are hidden until you ask for all objects`);
        }

        return `${lead}${rest.length ? ` The other ${(totals.viaTable + totals.never).toLocaleString()} of
            ${totals.total.toLocaleString()} objects are not controls: ${rest.join('; ')}.` : ''}`;
    }

    renderObjectPickerBanners() {
        const state = this.objectPicker;
        const host = document.getElementById('objPickerBanners');
        if (!host) return;

        const plan = state.plan;
        const changes = this.objectPickerChanges();
        const parts = [];

        // A paused module has no per-object story to tell: every verdict below
        // would read "module-disabled", so it is said once here instead.
        if (!plan.assignment || plan.assignment.enabled === false) {
            parts.push(`
                <p class="obj-picker-banner warn">Polling for ${this.escapeHtml(plan.mib)} is paused on
                ${this.escapeHtml(plan.device)}, so nothing from this module is collected and no object has a verdict yet.
                ${state.draft.enabled ? 'Save to start polling it and load the per-object verdicts.' : 'Turn the module on to choose objects.'}</p>
            `);
        } else if (!state.draft.enabled) {
            parts.push(`
                <p class="obj-picker-banner warn">Saving will pause ${this.escapeHtml(plan.mib)} on
                ${this.escapeHtml(plan.device)}. Nothing from this module will be collected, whatever is ticked below.</p>
            `);
        }

        // State what the picker can do now.
        if (plan.assignment && plan.assignment.enabled !== false && state.draft.enabled) {
            parts.push(`<p class="obj-picker-banner info howto">${this.objectPickerHowToHtml()}</p>`);
        }

        // Where the rule in force lives. A profile rule is shared, and editing
        // it here does not edit the profile.
        if (plan.source !== 'device') {
            const profiles = this.currentDeviceProfiles || [];
            const via = plan.source === 'profile'
                ? (profiles.length === 1 ? `the ${this.escapeHtml(profiles[0].name)} profile` : 'a profile')
                : 'the default (no stored assignment)';
            parts.push(`
                <p class="obj-picker-banner info source">These verdicts come from ${via}.
                Saving writes a device-level assignment for ${this.escapeHtml(plan.device)} that shadows it for this
                device only — other devices are unaffected, and this device stops following ${plan.source === 'profile' ? 'the profile' : 'the default'} for
                ${this.escapeHtml(plan.mib)}.</p>
            `);
        }

        if (changes.length) {
            parts.push(`
                <p class="obj-picker-banner pending">Unsaved: ${this.escapeHtml(changes.join(', '))}.
                The ticks show what you asked for; the poller's verdict is recomputed when you save.</p>
            `);
        }
        if (state.advancedEdited) {
            parts.push(`
                <p class="obj-picker-banner warn">You edited the patterns by hand. A pattern can match objects you did not
                tick, so the ticks below are still the last verdicts the server sent and may not match these patterns.
                Save to re-evaluate them.</p>
            `);
        }

        host.innerHTML = parts.join('');
    }

    bindObjectPicker() {
        const root = document.querySelector('#modalOverlayBody .mib-objects-picker');
        if (!root) return;

        const search = document.getElementById('objPickerSearch');
        search.addEventListener('input', () => {
            this.objectPicker.search = search.value;
            // Searching produces a different set, which is read from its start.
            this.objectPicker.page = 0;
            this.refreshObjectPickerList();
        });

        const show = document.getElementById('objPickerShow');
        show.addEventListener('change', () => {
            this.objectPicker.show = show.value;
            this.objectPicker.page = 0;
            this.refreshObjectPickerList();
        });

        const size = document.getElementById('objPickerSize');
        if (size) {
            size.addEventListener('change', () => {
                const next = parseInt(size.value, 10);
                if (!Number.isFinite(next) || next <= 0) return;
                const picker = this.objectPicker;
                picker.page = Math.floor((picker.page * picker.size) / next);
                picker.size = next;
                this.setPagerSize('objectPicker', next);
                this.refreshObjectPickerList();
            });
        }

        this.bindPager(document.getElementById('objPickerFooter'), (page) => {
            this.objectPicker.page = page;
            this.refreshObjectPickerList();
            // The list is its own scroll container, so a new page starts at its
            // top rather than wherever the last one was left.
            const list = document.getElementById('objPickerList');
            if (list) list.scrollTop = 0;
        });

        const sort = document.getElementById('objPickerSort');
        sort.addEventListener('change', () => {
            this.objectPicker.sort = sort.value;
            this.refreshObjectPickerList();
        });

        const enabled = document.getElementById('objPickerEnabled');
        enabled.addEventListener('change', () => {
            this.objectPicker.draft.enabled = enabled.checked;
            this.refreshObjectPickerList();
        });

        root.addEventListener('change', (event) => {
            const box = event.target.closest('input[data-object-oid]');
            if (!box) return;
            this.toggleObjectSelection(box.dataset.objectOid, box.checked);
        });

        root.addEventListener('click', (event) => {
            const cols = event.target.closest('[data-cols]');
            if (cols) {
                this.toggleObjectGroup(cols.dataset.cols);
                return;
            }
            const showAll = event.target.closest('#objPickerShowAllBtn');
            if (showAll) {
                this.setObjectPickerShow('all');
                return;
            }
            const showActionable = event.target.closest('#objPickerShowActionableBtn');
            if (showActionable) {
                this.setObjectPickerShow('actionable');
                return;
            }
            const excludeTable = event.target.closest('[data-exclude-table]');
            if (excludeTable) {
                this.excludeTableForObject(excludeTable.dataset.excludeTable);
            }
        });

        // Hand edits are the one place the ticks and the patterns can disagree
        // before a save, so they are recorded and called out.
        const patternFields = [
            ['objPickerIncludeNames', 'include_names'],
            ['objPickerExcludeNames', 'exclude_names'],
            ['objPickerIncludeOids', 'include_oids'],
            ['objPickerExcludeOids', 'exclude_oids']
        ];
        for (const [id, key] of patternFields) {
            const el = document.getElementById(id);
            el.addEventListener('input', () => {
                this.objectPicker.draft[key] = el.value.split('\n').map(l => l.trim()).filter(l => l.length > 0);
                this.objectPicker.advancedEdited = true;
                this.afterObjectPickerEdit();
            });
        }
        const maxOids = document.getElementById('objPickerMaxOids');
        maxOids.addEventListener('input', () => {
            const parsed = parseInt(maxOids.value, 10);
            this.objectPicker.draft.max_oids = Number.isNaN(parsed) || parsed < 0 ? 0 : parsed;
            this.objectPicker.advancedEdited = true;
            this.afterObjectPickerEdit();
        });

        const advanced = document.getElementById('objPickerAdvanced');
        advanced.addEventListener('toggle', () => { this.objectPicker.advancedOpen = advanced.open; });

        document.getElementById('objPickerCancelBtn').addEventListener('click', () => this.closeObjectPicker());
        document.getElementById('objPickerSaveBtn').addEventListener('click', () => this.saveObjectPicker());
    }

    // ---- saving ----------------------------------------------------------

    // validateObjectPickerFields re-reads the Advanced fields and compiles
    // every line. Nothing is sent while a pattern is invalid.
    validateObjectPickerFields() {
        const fields = {
            include_names: 'objPickerIncludeNames',
            exclude_names: 'objPickerExcludeNames',
            include_oids: 'objPickerIncludeOids',
            exclude_oids: 'objPickerExcludeOids'
        };

        let ok = true;
        const parsed = {};
        for (const [key, id] of Object.entries(fields)) {
            const lines = this.validateFilterField(id);
            if (lines === null) {
                ok = false;
            } else {
                parsed[key] = lines;
            }
        }

        const maxOidsInput = document.getElementById('objPickerMaxOids');
        const maxOidsError = document.getElementById('objPickerMaxOidsError');
        const raw = maxOidsInput.value.trim();
        let maxOids = 0;
        if (raw !== '') {
            maxOids = parseInt(raw, 10);
            if (Number.isNaN(maxOids) || maxOids < 0) {
                maxOidsInput.classList.add('input-error');
                maxOidsError.textContent = 'Max OIDs must be a non-negative whole number';
                maxOidsError.classList.add('visible');
                ok = false;
            }
        }
        if (ok) {
            maxOidsInput.classList.remove('input-error');
            maxOidsError.textContent = '';
            maxOidsError.classList.remove('visible');
            parsed.max_oids = maxOids;
        }

        if (!ok) {
            document.getElementById('objPickerAdvanced').open = true;
            return null;
        }
        return parsed;
    }

    async saveObjectPicker() {
        const state = this.objectPicker;
        if (!state || state.saving) return;

        const parsed = this.validateObjectPickerFields();
        if (parsed === null) {
            this.showNotification('Fix the invalid pattern(s) before saving', 'error');
            return;
        }
        Object.assign(state.draft, parsed);

        // A profile or default rule is shared. The write is always to the
        // device, so it is confirmed before it shadows anything.
        if (state.plan.source !== 'device' && !state.shadowConfirmed) {
            this.renderObjectPickerShadowConfirm();
            return;
        }

        const saveBtn = document.getElementById('objPickerSaveBtn');
        const list = document.getElementById('objPickerList');
        state.scrollTop = list ? list.scrollTop : 0;
        state.saving = true;
        if (saveBtn) saveBtn.disabled = true;

        try {
            await this.saveMibAssignment('device', state.deviceId, { id: state.mibId, name: state.mibName, assignment: {} }, {
                enabled: state.draft.enabled,
                include_names: state.draft.include_names,
                exclude_names: state.draft.exclude_names,
                include_oids: state.draft.include_oids,
                exclude_oids: state.draft.exclude_oids,
                max_oids: state.draft.max_oids
            });

            this.showNotification(`Object selection saved for ${state.mibName}`, 'success');
            await this.reloadMibList('device', state.deviceId);

            // The new verdicts come from the server, never from predicting
            // what the patterns just written will do.
            state.saving = false;
            await this.loadMibObjectPlan();
        } catch (error) {
            console.error('Error saving object selection:', error);
            this.showNotification(`Failed to save object selection for ${state.mibName}`, 'error');
            state.saving = false;
            if (saveBtn) saveBtn.disabled = false;
        }
    }

    renderObjectPickerShadowConfirm() {
        const state = this.objectPicker;
        const host = document.getElementById('objPickerConfirm');
        if (!host) return;

        const profiles = this.currentDeviceProfiles || [];
        const via = state.plan.source === 'profile'
            ? (profiles.length === 1 ? `the ${this.escapeHtml(profiles[0].name)} profile` : 'a profile')
            : 'no stored assignment';

        host.innerHTML = `
            <div class="obj-picker-banner warn" role="alertdialog" aria-label="Confirm device-level override">
                <p>${this.escapeHtml(state.plan.mib)} is currently governed by ${via}.
                Saving creates a device-level assignment on ${this.escapeHtml(state.plan.device)} which shadows it for that
                device only. Other devices keep their current behaviour, and later profile edits will no longer reach
                ${this.escapeHtml(state.plan.device)} for this module.</p>
                <div class="actions">
                    <button type="button" class="secondary-btn" id="objPickerShadowCancelBtn">Keep following ${state.plan.source === 'profile' ? 'the profile' : 'the default'}</button>
                    <button type="button" class="primary-btn" id="objPickerShadowConfirmBtn">Save to this device</button>
                </div>
            </div>
        `;

        document.getElementById('objPickerShadowCancelBtn').addEventListener('click', () => {
            host.innerHTML = '';
        });
        const confirmBtn = document.getElementById('objPickerShadowConfirmBtn');
        confirmBtn.addEventListener('click', () => {
            state.shadowConfirmed = true;
            host.innerHTML = '';
            this.saveObjectPicker();
        });
        confirmBtn.focus();
    }

    // renderMibAssignmentRow renders one editable row for a directly-assigned
    // MIB: the enable/disable switch, a filter summary, and the Filters
    // button, alongside whatever action (Remove) the caller supplies.
    renderMibAssignmentRow(scope, entityId, mib, { onRemove }) {
        const assignment = mib.assignment || {};
        const enabled = !!assignment.enabled;
        const filterSummary = this.summarizeMibFilters(assignment);
        const name = this.escapeHtml(mib.name);
        const switchLabel = `${enabled ? 'Disable' : 'Enable'} ${mib.name} polling`;

        // The object plan is computed per device, so the picker is offered on
        // the device page only; a profile row has no device to plan against.
        const objectsAction = scope === 'device'
            ? `<button class="secondary-btn" onclick="app.openMibObjectPicker('${entityId}', ${mib.id})">Objects</button>`
            : '';

        return `
            <div class="list-item mib-row">
                <div class="mib-row-main">
                    <label class="switch">
                        <input type="checkbox" class="switch-input" ${enabled ? 'checked' : ''}
                            aria-label="${this.escapeHtml(switchLabel)}"
                            onchange="app.toggleDirectMib('${scope}', '${entityId}', ${mib.id}, this)">
                        <span class="switch-track" aria-hidden="true"></span>
                    </label>
                    <div class="mib-row-info">
                        <strong>${name}</strong>
                        ${filterSummary ? `<span class="mib-filter-summary muted mono">${this.escapeHtml(filterSummary)}</span>` : ''}
                    </div>
                </div>
                <div class="actions">
                    ${objectsAction}
                    <button class="secondary-btn" onclick="app.openMibFilterModal('${scope}', '${entityId}', ${mib.id})">Filters</button>
                    <button class="danger-btn" onclick="${onRemove}">Remove</button>
                </div>
            </div>
        `;
    }

    // renderEffectiveMibRow renders a read-only effective MIB row; profile-sourced rows link to the profile.
    renderEffectiveMibRow(deviceId, mib, directIds) {
        const assignment = mib.assignment || {};
        const enabled = !!assignment.enabled;
        const isDirect = directIds.has(mib.id);
        const name = this.escapeHtml(mib.name);

        // Objects is offered on every applied MIB, including the ones a
        // profile supplies: choosing objects there is exactly the case that
        // needs the shadowing warning, and the picker gives it.
        const objectsBtn = `<button class="secondary-btn" onclick="app.openMibObjectPicker('${deviceId}', ${mib.id})">Objects</button>`;

        let sourceAction = '';
        if (!isDirect) {
            const profiles = this.currentDeviceProfiles || [];
            const editOnProfile = profiles.length === 1
                ? `<button class="secondary-btn" onclick="app.showProfileDetails('${profiles[0].id}')">Edit on ${this.escapeHtml(profiles[0].name)}</button>`
                : '<span class="muted">Edit on the source profile</span>';
            sourceAction = `<div class="actions">${objectsBtn}${editOnProfile}</div>`;
        } else {
            sourceAction = `<div class="actions">${objectsBtn}</div>`;
        }

        return `
            <div class="list-item mib-row">
                <div class="mib-row-main">
                    <span class="badge ${enabled ? 'enabled' : 'disabled'}">${enabled ? 'Enabled' : 'Disabled'}</span>
                    <div class="mib-row-info">
                        <strong>${name}</strong>
                        <span class="tag-badge">${isDirect ? 'direct' : 'profile'}</span>
                    </div>
                </div>
                ${sourceAction}
            </div>
        `;
    }

    async showAssignProfileModal() {
        const deviceId = this.currentDevice.id;

        try {
            const response = await fetch('/api/profiles');
            const data = await response.json();
            const allProfiles = data.profiles || [];

            document.getElementById('assignModalTitle').textContent = 'Assign Profile';

            this.renderPagedList('assignModalBody', {
                items: allProfiles,
                noun: 'profiles',
                placeholder: 'Search profiles…',
                matches: (profile, term) =>
                    `${profile.name || ''} ${profile.description || ''}`.toLowerCase().includes(term),
                renderRows: items => items.map(profile => `
                    <div class="list-item">
                        <div class="list-item-text">
                            <strong>${this.escapeHtml(profile.name)}</strong>
                            ${this.descriptionCellHtml(profile.description, profile.name, 'Profile')}
                        </div>
                        <button class="primary-btn" onclick="app.assignProfileToDevice('${deviceId}', '${profile.id}'); app.closeModal('assignModal');">Assign</button>
                    </div>
                `).join(''),
                emptyHtml: '<p class="empty-state">No profiles defined yet</p>'
            });

            this.showModal('assignModal');
        } catch (error) {
            console.error('Error loading profiles:', error);
            this.showNotification('Failed to load profiles', 'error');
        }
    }

    async showAssignMIBModal() {
        if (!this.currentDevice) {
            this.showNotification('Please select a device first', 'error');
            return;
        }
        const deviceId = this.currentDevice.id;

        try {
            const response = await fetch('/api/mibs');
            const data = await response.json();
            const allMibs = data.mibs || [];

            document.getElementById('assignModalTitle').textContent = 'Assign MIB';

            this.renderPagedList('assignModalBody', {
                items: allMibs,
                noun: 'modules',
                placeholder: 'Search modules by name or tag…',
                matches: (mib, term) =>
                    `${mib.name || ''} ${(mib.tags || []).join(' ')}`.toLowerCase().includes(term),
                renderRows: items => items.map(mib => `
                    <div class="list-item">
                        <div class="list-item-text">
                            <strong>${this.escapeHtml(mib.name)}</strong>
                            <span class="muted">${(mib.object_count || 0).toLocaleString()} object${(mib.object_count || 0) === 1 ? '' : 's'}</span>
                        </div>
                        <button class="primary-btn" onclick="app.assignMIBToDevice('${deviceId}', '${mib.id}'); app.closeModal('assignModal');">Assign</button>
                    </div>
                `).join(''),
                emptyHtml: '<p class="empty-state">No MIBs imported yet</p>'
            });

            this.showModal('assignModal');
        } catch (error) {
            console.error('Error loading MIBs:', error);
            this.showNotification('Failed to load MIBs', 'error');
        }
    }

    async showAssignMIBToProfileModal() {
        if (!this.currentProfile) {
            this.showNotification('Please select a profile first', 'error');
            return;
        }
        const profileId = this.currentProfile.id;

        try {
            const response = await fetch('/api/mibs');
            const data = await response.json();
            const allMibs = data.mibs || [];

            document.getElementById('assignModalTitle').textContent = 'Assign MIB';

            this.renderPagedList('assignModalBody', {
                items: allMibs,
                noun: 'modules',
                placeholder: 'Search modules by name or tag…',
                matches: (mib, term) =>
                    `${mib.name || ''} ${(mib.tags || []).join(' ')}`.toLowerCase().includes(term),
                renderRows: items => items.map(mib => `
                    <div class="list-item">
                        <div class="list-item-text">
                            <strong>${this.escapeHtml(mib.name)}</strong>
                            <span class="muted">${(mib.object_count || 0).toLocaleString()} object${(mib.object_count || 0) === 1 ? '' : 's'}</span>
                        </div>
                        <button class="primary-btn" onclick="app.assignMIBToProfile('${profileId}', '${mib.id}'); app.closeModal('assignModal');">Assign</button>
                    </div>
                `).join(''),
                emptyHtml: '<p class="empty-state">No MIBs imported yet</p>'
            });

            this.showModal('assignModal');
        } catch (error) {
            console.error('Error loading MIBs:', error);
            this.showNotification('Failed to load MIBs', 'error');
        }
    }

    async showBulkAssignMIBModal(deviceId) {
        const device = this.devices.find(d => d.id === deviceId);
        if (!device) {
            this.showNotification('Device not found', 'error');
            return;
        }
        
        try {
            // Fetch all available MIBs and currently assigned MIBs
            const availableResponse = await fetch('/api/mibs');
            const availableData = await availableResponse.json();
            const availableMIBs = availableData.mibs || [];
            
            const assignedResponse = await fetch(`/api/devices/${deviceId}/mibs`);
            const assignedData = await assignedResponse.json();
            const assignedMIBs = assignedData.all_mibs || [];
            const assignedIds = new Set(assignedMIBs.map(m => m.id));
            
            const unassignedMIBs = availableMIBs.filter(m => !assignedIds.has(m.id));
            
            if (unassignedMIBs.length === 0) {
                this.showNotification('All available MIBs are already assigned to this device', 'info');
                return;
            }
            
            // Show modal
            const modalOverlay = document.getElementById('modalOverlay');
            const modalTitle = document.getElementById('modalOverlayTitle');
            const modalBody = document.getElementById('modalOverlayBody');
            
            modalTitle.textContent = 'Assign Multiple MIBs';
            modalBody.innerHTML = `
                <form id="bulkAssignMibForm">
                    <div class="form-group">
                        <label><strong>Device:</strong> ${this.escapeHtml(device.name)}</label>
                    </div>
                    <div class="form-group">
                        <label>Select MIBs to assign</label>
                        <div class="checkbox-list" id="bulkAssignMibList"></div>
                    </div>
                    <p class="mib-objects-note" id="bulkAssignMibCount" role="status" aria-live="polite">
                        No modules selected
                    </p>
                    <div class="form-actions">
                        <button type="submit" class="primary-btn">Assign Selected</button>
                        <button type="button" class="secondary-btn" onclick="document.getElementById('modalOverlay').style.display='none'">Cancel</button>
                    </div>
                </form>
            `;

            // The list pages, so a tick has to live in the selection rather
            // than in the checkbox: the DOM node that carried it is gone as
            // soon as the reader searches for the next module.
            const selected = new Set();
            const countEl = document.getElementById('bulkAssignMibCount');
            const describeSelection = () => {
                countEl.textContent = selected.size === 0
                    ? 'No modules selected'
                    : `${selected.size.toLocaleString()} module${selected.size === 1 ? '' : 's'} selected`;
            };

            this.renderPagedList('bulkAssignMibList', {
                items: unassignedMIBs,
                noun: 'modules',
                placeholder: 'Search modules by name or tag…',
                matches: (mib, term) =>
                    `${mib.name || ''} ${(mib.tags || []).join(' ')}`.toLowerCase().includes(term),
                wrap: rows => `<div class="checkbox-rows">${rows}</div>`,
                renderRows: items => items.map(mib => `
                    <label class="checkbox-row">
                        <input type="checkbox" name="mibIds" value="${this.escapeAttr(String(mib.id))}"
                            ${selected.has(String(mib.id)) ? 'checked' : ''}>
                        <span>${this.escapeHtml(mib.name)}</span>
                        <span class="muted">${(mib.object_count || 0).toLocaleString()} object${(mib.object_count || 0) === 1 ? '' : 's'}</span>
                    </label>
                `).join(''),
                emptyHtml: '<p class="empty-state">Every imported module is already assigned</p>'
            });

            document.getElementById('bulkAssignMibList').addEventListener('change', (event) => {
                const box = event.target.closest('input[name="mibIds"]');
                if (!box) return;
                if (box.checked) selected.add(box.value); else selected.delete(box.value);
                describeSelection();
            });

            modalOverlay.style.display = 'block';

            // Handle form submission
            document.getElementById('bulkAssignMibForm').addEventListener('submit', async (e) => {
                e.preventDefault();

                if (selected.size === 0) {
                    this.showNotification('Please select at least one MIB', 'warning');
                    return;
                }

                await this.bulkAssignMIBsToDevice(deviceId, Array.from(selected));
                modalOverlay.style.display = 'none';
            });
        } catch (error) {
            console.error('Error showing bulk assign modal:', error);
            this.showNotification('Failed to load MIBs', 'error');
        }
    }
    
    async bulkAssignMIBsToDevice(deviceId, mibIds) {
        let successCount = 0;
        let failCount = 0;
        
        this.showNotification(`Assigning ${mibIds.length} MIB(s)...`, 'info');
        
        for (const mibId of mibIds) {
            try {
                await this.assignMIBToDevice(deviceId, mibId);
                successCount++;
            } catch (error) {
                console.error(`Failed to assign MIB ${mibId}:`, error);
                failCount++;
            }
        }
        
        if (failCount === 0) {
            this.showNotification(`Successfully assigned ${successCount} MIB(s)`, 'success');
        } else {
            this.showNotification(`Assigned ${successCount} MIB(s), ${failCount} failed`, 'warning');
        }
        
        // Refresh device MIBs
        await this.loadDeviceMIBs(deviceId);
    }

    // ========================================
    // Dashboard Generation
    // ========================================

    // generateDeviceDashboard opens a device's dashboard through the selector and reports which MIBs contributed panels.
    async generateDeviceDashboard(deviceId) {
        try {
            this.showNotification('Generating dashboard...', 'success');

            const response = await fetch(`/api/devices/${deviceId}/dashboard`, {
                method: 'GET'
            });

            if (!response.ok) {
                const errorText = await response.text();
                throw new Error(`HTTP error! status: ${response.status} - ${errorText}`);
            }

            const dashboardConfig = await response.json();
            console.log('Dashboard config:', dashboardConfig);

            this.switchView('dashboard');
            this.openFleetDevice(deviceId);

            // A new MIB contributes nothing until the device has been polled.
            const warnings = dashboardConfig.warnings || [];
            if (warnings.length > 0) {
                warnings.forEach(warning => this.showNotification(warning, 'warning', 15000));
            } else {
                const count = (dashboardConfig.panels || []).length;
                this.showNotification(`Dashboard generated: ${count} panels`, 'success');
            }
        } catch (error) {
            console.error('Error generating dashboard:', error);
            this.showNotification(`Failed to generate dashboard: ${error.message}`, 'error');
        }
    }

    async loadChartData(metric, chart, timeRange = '6h') {
        try {
            const response = await fetch('/api/metrics/query', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                    metric: metric,
                    time_range: timeRange
                })
            });

            if (!response.ok) {
                console.error(`Failed to load data for ${metric}:`, await response.text());
                return;
            }

            const seriesData = await response.json();
            console.log(`Loaded data for ${metric}:`, seriesData);

            // Update chart with historical data
            if (seriesData && seriesData.length > 0) {
                const series = seriesData[0]; // Use first series
                const chartData = (series.values || []).map(v => [
                    v[0] * 1000, // Convert Unix timestamp to milliseconds
                    parseFloat(v[1])
                ]);

                chart.setOption({
                    series: [{
                        data: chartData
                    }]
                });
            }
        } catch (error) {
            console.error(`Error loading chart data for ${metric}:`, error);
        }
    }

    // Route a query's result to the panels that subscribed with that exact query.
    updateDashboardCharts(query, metrics, timestamp) {
        if (!metrics || !Array.isArray(metrics)) {
            return;
        }

        // Points are plotted at the tick that carried them.
        const time = new Date(timestamp * 1000);

        this.activeSubscriptions.forEach((subscription) => {
            const target = subscription.objects.get(query);
            if (target && target.panel) {
                this.updatePanel(target, metrics, time);
            }
        });
    }

    // Chart options: one builder for a series over time; colours from netmonCharts.

    timeAxisOption(spanMs, data) {
        const option = {
            type: 'time',
            boundaryGap: false,
            // Vertical rules add nothing over a time series; the horizontal
            // value grid is what a reader measures against.
            splitLine: { show: false },
            minInterval: netmonCharts.timeAxisMinInterval(spanMs),
            axisLabel: {
                hideOverlap: true,
                formatter: netmonCharts.timeAxisFormatter(spanMs)
            }
        };

        // Pin a minute around a single sample; null once there are two.
        if (data && data.length === 1) {
            const at = new Date(data[0][0]).getTime();
            option.min = at - 30000;
            option.max = at + 30000;
        } else {
            option.min = null;
            option.max = null;
        }

        return option;
    }

    seriesSpan(data) {
        if (!data || data.length < 2) return 0;
        const first = new Date(data[0][0]).getTime();
        const last = new Date(data[data.length - 1][0]).getTime();
        return Math.max(0, last - first);
    }

    // Card data states: waiting, charting or terminal, held on the container.

    chartCard(chartId) {
        const element = document.getElementById(chartId);
        return element ? element.closest('.chart-container') : null;
    }

    // Back to waiting: used when a card is first built and when an instance
    // change starts a fresh subscription for the same card.
    resetChartCard(chartId) {
        const card = this.chartCard(chartId);
        if (!card) return;

        card.classList.add('is-empty');
        card.classList.remove('is-nodata');
        const message = card.querySelector('.chart-empty-title');
        const detail = card.querySelector('.chart-empty-detail');
        if (message) message.textContent = 'Waiting for data…';
        if (detail) detail.textContent = 'Listening for the first sample.';
    }

    revealChart(chartId) {
        const card = this.chartCard(chartId);
        if (!card) return;

        card.classList.remove('is-empty', 'is-nodata');
        // The placeholder is hidden from here on, but leaving "Waiting for
        // data…" sitting in it makes a charting card read as a waiting one to
        // anything that inspects text rather than classes.
        const stale = card.querySelector('.chart-empty-title');
        if (stale) stale.textContent = '';

        const chart = this.charts[chartId];
        if (chart && chart.resize) {
            // The canvas was collapsed while the card was empty, and the
            // instance still carries the provisional size it was created with.
            requestAnimationFrame(() => {
                try { chart.resize(AUTO_SIZE); } catch (e) { /* disposed mid-flight */ }
            });
        }
    }

    markChartNoData(chartId) {
        const card = this.chartCard(chartId);
        if (!card) return;

        card.classList.add('is-empty', 'is-nodata');
        const message = card.querySelector('.chart-empty-title');
        const detail = card.querySelector('.chart-empty-detail');
        if (message) message.textContent = NO_DATA_TITLE;
        if (detail) detail.textContent = NO_DATA_DETAIL;
    }

    // A status tile has the same three states as a chart card.
    statusTile(statusId) {
        const element = document.getElementById(statusId);
        return element ? element.closest('.status-item') : null;
    }

    markStatusNoData(statusId) {
        const element = document.getElementById(statusId);
        if (!element) return;

        element.textContent = NO_DATA_TITLE;
        element.title = NO_DATA_DETAIL;
        const tile = this.statusTile(statusId);
        if (tile) tile.classList.add('is-nodata');
    }

    // A late first sample still wins: the terminal state is what the UI knows
    // so far, not a claim that nothing can ever arrive.
    clearStatusNoData(element) {
        if (!element) return;
        element.removeAttribute('title');
        const tile = element.closest('.status-item');
        if (tile) tile.classList.remove('is-nodata');
    }

    // Metric names and queries: panels arrive with their PromQL from internal/webui/api/dashboard_gen.go.


    // ========================================
    // Helper: Check if object is a status/info type
    // ========================================

    isStatusObject(type, name) {
        // Always treat string types as status
        const stringTypes = [
            'DisplayString',
            'OCTET STRING',
            'SnmpAdminString',
            'IpAddress',
            'PhysAddress',
            'DateAndTime',
            'OBJECT IDENTIFIER',
            'InetAddress'
        ];

        if (stringTypes.some(t => type && type.includes(t))) {
            return true;
        }

        // Check for specific object names that are enumerations (not charts)
        if (name) {
            const nameLower = name.toLowerCase();
            const statusNames = [
                'descr',      // ifDescr, sysDescr, etc
                'name',       // ifName, etc
                'alias',      // ifAlias
                'type',       // ifType
                'status',     // ifAdminStatus, ifOperStatus
                'contact',    // sysContact
                'location',   // sysLocation
                'objectid',   // sysObjectID
                'uptime'      // sysUpTime (TimeTicks)
            ];

            if (statusNames.some(s => nameLower.includes(s))) {
                return true;
            }
        }

        return false;
    }

    // formatAxisValue shortens an axis tick using the catalog's declared unit.
    formatAxisValue(value, unit) {
        const number = parseFloat(value);
        if (isNaN(number)) return String(value ?? '');

        // A byte count scales by 1024; everything else by 1000.
        const binary = unit === 'octets' || unit === 'bytes';
        const step = binary ? 1024 : 1000;
        const suffixes = binary ? ['K', 'M', 'G', 'T'] : ['K', 'M', 'G', 'T'];

        const magnitude = Math.abs(number);
        for (let i = suffixes.length - 1; i >= 0; i--) {
            const scale = Math.pow(step, i + 1);
            if (magnitude >= scale) {
                return (number / scale).toFixed(1) + suffixes[i];
            }
        }

        // Below the compact thresholds the tick has to carry enough precision
        // that neighbouring ticks cannot round to the same string.
        return netmonCharts.formatTick(number);
    }
    // ========================================
    // MIB Browser (existing functionality)
    // ========================================

    // loadDevices refills the device list and selectors; restore: false keeps the selection, strict: true reports a failed read.
    async loadDevices({ restore = true, strict = false } = {}) {
        try {
            const response = await fetch('/api/devices');
            if (!response.ok) throw new Error(`HTTP ${response.status}`);
            const data = await response.json();

            // Keep the device list for deviceNameFor.
            this.devices = data.devices || [];

            // effective_poll_interval arrives with this list, so the cadence
            // line can only be truthful once it has landed.
            this.renderDashboardCadence();

            // Populate legacy device selector (if it exists)
            const select = document.getElementById('deviceSelect');
            if (select) {
                select.innerHTML = '<option value="all">All Devices</option>';
                if (data.devices) {
                    data.devices.forEach(device => {
                        const option = document.createElement('option');
                        option.value = device.id;
                        option.textContent = device.name;
                        select.appendChild(option);
                    });
                }
            }

            // Populate dashboard device selector
            const dashboardSelect = document.getElementById('dashboardDeviceSelect');
            if (dashboardSelect) {
                // The empty option is a destination now, not a placeholder: it
                // is the way back from a device to the fleet.
                dashboardSelect.innerHTML = '<option value="">All devices — fleet overview</option>';
                if (data.devices) {
                    data.devices.forEach(device => {
                        const option = document.createElement('option');
                        option.value = device.id;
                        option.textContent = device.name;
                        dashboardSelect.appendChild(option);
                    });
                }
            }

            // Restore state after devices are loaded
            if (restore) this.restoreState();
        } catch (error) {
            console.error('Error loading devices:', error);
            if (strict) throw error;
        }
    }

    // ========================================
    // MIB Browser Accordion
    // ========================================

    // Renders the module list from /api/mibs alone. Every row starts collapsed
    // and carries no request of its own; details arrive on first expand.
    async loadMIBsAccordion(force = false) {
        const accordion = document.getElementById('mibAccordion');
        if (!accordion) return;

        // Re-opening the view reuses what is already on screen, including any
        // module the user had expanded.
        if (!force && this.mibsData && accordion.querySelector('.mib-accordion-item')) {
            return;
        }

        accordion.innerHTML = `
            <div class="mib-loading">
                <div class="spinner"></div>
                Loading MIBs…
            </div>
        `;

        try {
            const response = await fetch('/api/mibs');
            if (!response.ok) throw new Error(`HTTP ${response.status}`);
            const data = await response.json();
            const mibs = data.mibs || [];
            this.mibsData = mibs;

            if (mibs.length === 0) {
                accordion.innerHTML = `
                    <div class="mib-accordion-empty">
                        <h4>No MIBs imported</h4>
                        <p>Import modules from the MIBs view and they will appear here.</p>
                    </div>
                `;
                this.updateMIBBrowserSummary();
                return;
            }

            // The MIB library is paged.
            this.pagedLists.delete('mibAccordion');
            this.renderPagedList('mibAccordion', {
                items: mibs,
                noun: 'modules',
                pageSize: MIB_ACCORDION_PAGE,
                sizeKey: 'mibAccordion',
                // The browser's own toolbar already holds a filter box beside
                // Expand and Collapse, which is where a reader looks for it.
                searchInput: 'mibSearchInput',
                matches: (mib, term) => String(mib.name || '').toLowerCase().includes(term),
                sortColumns: this.mibAccordionSortColumns(),
                sortKey: 'name',
                sortDir: 'asc',
                wrap: (rows, mountId) => `
                    <div class="mib-accordion-sort">${this.sortControlsHtml(mountId)}</div>
                    <div class="mib-accordion-items">${rows}</div>`,
                renderRows: items => items.map(mib => this.renderMIBAccordionItem(mib)).join(''),
                afterPaint: () => {
                    this.bindMIBAccordion(document.getElementById('mibAccordion'));
                    this.updateMIBBrowserSummary();
                }
            });
        } catch (error) {
            console.error('Error loading MIBs:', error);
            this.mibsData = null;
            accordion.innerHTML = `
                <div class="mib-accordion-empty">
                    <h4>Could not load MIBs</h4>
                    <p>${this.escapeHtml(error.message)}</p>
                </div>
            `;
        }
    }

    // One delegated listener for the whole list, installed once, instead of a
    // handler per module.
    bindMIBAccordion(accordion) {
        if (this.mibAccordionBound) return;

        accordion.addEventListener('click', (event) => {
            const header = event.target.closest('.mib-accordion-header');
            if (header) {
                this.toggleMIBAccordion(header.closest('.mib-accordion-item').dataset.mibId);
            }
        });
        this.mibAccordionBound = true;
    }

    renderMIBAccordionItem(mib) {
        const objectCount = mib.object_count || mib.objectCount || 0;
        const tags = mib.tags || [];
        const tagsHtml = tags.length > 0
            ? `<span class="mib-accordion-tags">${tags.map(t => `<span class="tag-badge">${this.escapeHtml(t)}</span>`).join('')}</span>`
            : '';

        // Zero objects is correct for TC and conformance-only modules.
        const meta = objectCount === 0
            ? 'No objects — definitions only'
            : `${objectCount.toLocaleString()} object${objectCount === 1 ? '' : 's'}`;

        return `
            <div class="mib-accordion-item" data-mib-id="${mib.id}"
                 data-mib-name="${this.escapeAttr(String(mib.name || '').toLowerCase())}">
                <button type="button" class="mib-accordion-header"
                        aria-expanded="false" aria-controls="mib-content-${mib.id}">
                    <span class="mib-accordion-toggle" aria-hidden="true">&#9654;</span>
                    <span class="mib-accordion-title">
                        <span class="mib-accordion-name">${this.escapeHtml(mib.name)}</span>
                        <span class="mib-accordion-meta">${meta}</span>
                        ${tagsHtml}
                    </span>
                </button>
                <div class="mib-accordion-content" id="mib-content-${mib.id}">
                    <div class="mib-accordion-body"></div>
                </div>
            </div>
        `;
    }

    async toggleMIBAccordion(mibId) {
        const item = document.querySelector(`.mib-accordion-item[data-mib-id="${mibId}"]`);
        if (!item) return;

        if (item.classList.contains('expanded')) {
            this.setMIBAccordionExpanded(item, false);
            return;
        }
        this.setMIBAccordionExpanded(item, true);
        await this.renderMIBAccordionDetails(item);
    }

    setMIBAccordionExpanded(item, expanded) {
        item.classList.toggle('expanded', expanded);
        const header = item.querySelector('.mib-accordion-header');
        if (header) header.setAttribute('aria-expanded', expanded ? 'true' : 'false');
    }

    // Fills a module's body the first time it is opened. Collapsing leaves the
    // rendered objects in place, so re-opening is instant and costs no request.
    async renderMIBAccordionDetails(item) {
        const body = item.querySelector('.mib-accordion-body');
        if (!body || body.dataset.rendered === 'true') return;

        body.innerHTML = `
            <div class="mib-loading">
                <div class="spinner"></div>
                Loading objects…
            </div>
        `;

        try {
            const mib = await this.fetchMIBDetail(item.dataset.mibId);
            this.renderMIBObjects(body, mib);
            body.dataset.rendered = 'true';
        } catch (error) {
            console.error('Error loading MIB details:', error);
            // Left unmarked so the next expand retries.
            body.innerHTML = `<p class="mib-accordion-error">Could not load objects: ${this.escapeHtml(error.message)}</p>`;
        }
    }

    // Caches by module id and shares one in-flight request per module.
    fetchMIBDetail(mibId) {
        const key = String(mibId);
        if (this.mibDetails.has(key)) return Promise.resolve(this.mibDetails.get(key));

        let pending = this.mibDetailRequests.get(key);
        if (!pending) {
            pending = fetch(`/api/mibs/${encodeURIComponent(key)}`)
                .then((response) => {
                    if (!response.ok) throw new Error(`HTTP ${response.status}`);
                    return response.json();
                })
                .then((mib) => {
                    this.mibDetails.set(key, mib);
                    return mib;
                })
                .finally(() => this.mibDetailRequests.delete(key));
            this.mibDetailRequests.set(key, pending);
        }
        return pending;
    }

    renderMIBObjects(body, mib) {
        const objects = mib.objects || [];
        const mountId = `mib-objects-${mib.id}`;
        const parts = [];

        if (mib.description) {
            parts.push(`<div class="mib-description">${this.escapeHtml(mib.description)}</div>`);
        }
        parts.push(`
            <div class="mib-objects-header">
                <h5>Objects</h5>
                <span class="mib-objects-count">${objects.length.toLocaleString()} total</span>
            </div>
        `);

        if (objects.length === 0) {
            parts.push(`
                <p class="mib-objects-note">
                    This module declares no managed objects. Modules that carry only textual
                    conventions or conformance groups are expected to be empty here.
                </p>
            `);
            body.innerHTML = parts.join('');
            return;
        }

        parts.push(`<div id="${mountId}"></div>`);
        body.innerHTML = parts.join('');

        // Module objects are searched and paged; the pager is named after the module.
        this.renderPagedList(mountId, {
            items: objects,
            pageSize: MIB_OBJECT_PAGE,
            sizeKey: 'mibObjects',
            pagerLabel: `${mib.name} objects`,
            noun: 'objects',
            placeholder: 'Search objects by name, OID or description…',
            matches: (obj, term) => this.objectMatchesSearch(obj, term),
            wrap: rows => `<div class="mib-objects-list">${rows}</div>`,
            renderRows: items => this.renderMIBObjectRows(items, 0, items.length)
        });
    }

    renderMIBObjectRows(objects, from, to) {
        let html = '';
        for (let i = from; i < to; i++) {
            const obj = objects[i];
            // The description cell is the control that opens the sheet, so the
            // clipped line is the thing you click rather than a dead ellipsis
            // with the real text hidden in a tooltip.
            const description = this.descriptionTriggerHtml(obj.description, {
                title: obj.name,
                eyebrow: 'SNMP object',
                className: 'mib-object-description',
                meta: [
                    ['OID', obj.oid],
                    ['Syntax', obj.type],
                    ['Access', obj.access],
                    ['Kind', obj.kind]
                ]
            }) || '<span class="mib-object-description is-undescribed">No DESCRIPTION in the module</span>';

            html += `
                <div class="mib-object-item">
                    <div class="mib-object-name">${this.escapeHtml(obj.name)}</div>
                    <div class="mib-object-oid">${this.escapeHtml(obj.oid || 'N/A')}</div>
                    ${description}
                </div>
            `;
        }
        return html;
    }

    // Filters the collapsed list by module name without requests.
    // MIB browser sort keys: name (default), object count, imported date.
    mibAccordionSortColumns() {
        return [
            { key: 'name', label: 'Name', firstDir: 'asc', get: mib => mib.name || '' },
            {
                key: 'objects', label: 'Objects', firstDir: 'desc',
                get: mib => typeof mib.object_count === 'number' ? mib.object_count : null
            },
            {
                key: 'imported', label: 'Imported', firstDir: 'desc',
                get: mib => {
                    const at = Date.parse(mib.imported_at || '');
                    return Number.isFinite(at) ? at : null;
                }
            }
        ];
    }


    // The summary reads the list's own state.
    updateMIBBrowserSummary() {
        const summary = document.getElementById('mibBrowserSummary');
        if (!summary) return;

        const mibs = this.mibsData || [];
        const objects = mibs.reduce((sum, mib) => sum + (mib.object_count || 0), 0);
        const state = this.pagedLists.get('mibAccordion');
        const term = state ? state.search.trim() : '';

        // textContent, so a search term cannot inject markup.
        if (!term) {
            summary.textContent =
                `${mibs.length.toLocaleString()} modules · ${objects.toLocaleString()} objects. ` +
                'Expand a module to load its objects.';
            return;
        }

        const lowered = term.toLowerCase();
        const visible = mibs.filter(mib => String(mib.name || '').toLowerCase().includes(lowered)).length;
        summary.textContent =
            `${visible.toLocaleString()} of ${mibs.length.toLocaleString()} modules match “${term}”.`;
    }

    // Expanding is one request per module, so this works on the filtered list,
    // stops well short of the whole library, and fans out a few at a time.
    async expandAllMIBAccordion() {
        const accordion = document.getElementById('mibAccordion');
        if (!accordion) return;

        // Expand the current page only: one request per module.
        const targets = Array.from(
            accordion.querySelectorAll('.mib-accordion-item:not(.expanded)'));
        if (targets.length === 0) return;

        if (targets.length > MIB_EXPAND_ALL_LIMIT) {
            this.showNotification(
                `Expand all covers up to ${MIB_EXPAND_ALL_LIMIT} modules and this page lists ` +
                `${targets.length}. Choose a smaller page size or filter the list first.`, 'warning');
            return;
        }

        const queue = targets.slice();
        const worker = async () => {
            for (let item = queue.shift(); item; item = queue.shift()) {
                this.setMIBAccordionExpanded(item, true);
                await this.renderMIBAccordionDetails(item);
            }
        };
        const workers = Math.min(MIB_EXPAND_ALL_CONCURRENCY, queue.length);
        await Promise.all(Array.from({ length: workers }, worker));
    }

    collapseAllMIBAccordion() {
        const accordion = document.getElementById('mibAccordion');
        if (!accordion) return;

        accordion.querySelectorAll('.mib-accordion-item.expanded')
            .forEach(item => this.setMIBAccordionExpanded(item, false));
    }

    // Importing or deleting modules invalidates both the list and the cached
    // details; the next visit to the browser rebuilds them.
    invalidateMIBBrowser() {
        this.mibsData = null;
        this.mibDetails.clear();
        this.mibDetailRequests.clear();
    }

    // Legacy function - kept for backward compatibility
    async loadMIBs() {
        await this.loadMIBsAccordion();
    }

    // Legacy function - kept for backward compatibility
    async loadMIBDetails(id) {
        const item = document.querySelector(`.mib-accordion-item[data-mib-id="${id}"]`);
        if (item && !item.classList.contains('expanded')) {
            await this.toggleMIBAccordion(id);
        }
    }

    // ========================================
    // Modal Management
    // ========================================

    setupModals() {
        // The description sheet is opened from rows all over the app, so its
        // listeners are delegated once here rather than per surface.
        this.bindDescriptionSheet();

        // Device form
        const versionSelect = document.getElementById('deviceSNMPVersion');
        if (versionSelect) {
            versionSelect.addEventListener('change', () => this.syncDeviceVersionFields());
        }

        document.getElementById('deviceForm').addEventListener('submit', (e) => {
            e.preventDefault();
            const formData = new FormData(e.target);
            this.saveDevice(formData);
        });

        // Profile form
        document.getElementById('profileForm').addEventListener('submit', (e) => {
            e.preventDefault();
            const formData = new FormData(e.target);
            this.saveProfile(formData);
        });

        // Close buttons
        document.querySelectorAll('.close-modal').forEach(btn => {
            btn.addEventListener('click', (e) => {
                const modal = e.target.closest('.modal');
                if (modal) {
                    modal.classList.remove('active');
                }
            });
        });

        // Cancel buttons
        document.querySelectorAll('.cancel-btn').forEach(btn => {
            btn.addEventListener('click', (e) => {
                const modal = e.target.closest('.modal');
                if (modal) {
                    modal.classList.remove('active');
                }
            });
        });

        // Click outside to close
        document.querySelectorAll('.modal').forEach(modal => {
            modal.addEventListener('click', (e) => {
                if (e.target === modal) {
                    modal.classList.remove('active');
                }
            });
        });
    }

    showModal(modalId) {
        document.getElementById(modalId).classList.add('active');
    }

    closeModal(modalId) {
        document.getElementById(modalId).classList.remove('active');
    }

    // ========================================
    // Event Listeners
    // ========================================

    setupEventListeners() {
        // Dashboard controls
        document.getElementById('dashboardDeviceSelect').addEventListener('change', (e) => {
            const deviceId = e.target.value;

            // Unsubscribe from previous device's dashboards
            if (this.currentDashboardDevice && this.currentDashboardDevice !== deviceId) {
                this.unsubscribeFromAllDashboards(this.currentDashboardDevice);
            }

            if (deviceId) {
                this.currentDashboardDevice = deviceId;
                this.renderDashboardCadence();
                this.loadDeviceDashboards(deviceId);
                this.saveState();
            } else {
                // Clearing the selection goes back to the fleet, not to a blank
                // frame asking to be told what to look at.
                this.currentDashboardDevice = null;
                this.renderDashboardCadence();
                this.hideDeviceStatusBanner();
                this.showFleetOverview();
                this.saveState();
            }
        });

        document.getElementById('dashboardTimeRange').addEventListener('change', (e) => {
            this.currentTimeRange = e.target.value;
            this.saveState();
            // Note: Time range only affects historical data loading
            // WebSocket streams are always real-time
        });

        // Device management
        document.getElementById('addDeviceBtn').addEventListener('click', () => this.showAddDeviceModal());
        document.getElementById('backToDevicesBtn').addEventListener('click', () => {
            this.switchView('devices');
            this.loadStoredDevices();
        });
        document.getElementById('editDeviceBtn').addEventListener('click', () => {
            if (this.currentDevice) {
                this.showEditDeviceModal(this.currentDevice.id);
            }
        });
        document.getElementById('testSnmpBtn').addEventListener('click', () => {
            if (this.currentDevice) {
                this.testSNMPConnection(this.currentDevice.id);
            }
        });
        document.getElementById('assignProfileBtn').addEventListener('click', () => this.showAssignProfileModal());
        document.getElementById('assignMibBtn').addEventListener('click', () => this.showAssignMIBModal());
        document.getElementById('bulkAssignMibBtn').addEventListener('click', () => this.showBulkAssignMIBModal(this.currentDevice.id));
        document.getElementById('generateDashboardBtn').addEventListener('click', () => {
            if (this.currentDevice) {
                this.generateDeviceDashboard(this.currentDevice.id);
            }
        });

        // Profile management
        document.getElementById('addProfileBtn').addEventListener('click', () => this.showAddProfileModal());
        document.getElementById('backToProfilesBtn').addEventListener('click', () => {
            this.switchView('profiles');
            this.loadProfiles();
        });
        document.getElementById('editProfileBtn').addEventListener('click', () => {
            if (this.currentProfile) {
                this.showEditProfileModal(this.currentProfile.id);
            }
        });
        document.getElementById('assignMibToProfileBtn').addEventListener('click', () => this.showAssignMIBToProfileModal());

        // MIB upload - show import dialog
        document.getElementById('uploadMibBtn').addEventListener('click', () => {
            this.showMibImportModal();
        });

        // Legacy file input (keep for backwards compatibility)
        document.getElementById('mibFileInput').addEventListener('change', (e) => {
            if (e.target.files.length > 0) {
                this.handleBulkMIBUpload(e.target.files, []);
                e.target.value = '';
            }
        });

        // MIB Import Modal handlers
        this.setupMibImportModal();

        // Other (legacy elements - may not exist)
        const deviceSelect = document.getElementById('deviceSelect');
        if (deviceSelect) {
            deviceSelect.addEventListener('change', (e) => {
                console.log('Device filter changed:', e.target.value);
            });
        }

        const saveSettings = document.getElementById('saveSettings');
        if (saveSettings) {
            saveSettings.addEventListener('click', () => {
                // Apply and persist the interval, and report the value that took effect.
                const input = document.getElementById('updateInterval');
                const applied = this.applyRefreshInterval(input ? input.value : this.refreshIntervalSec);
                if (input) input.value = applied;
                this.saveState();
                this.showNotification(`Dashboard now refreshes every ${applied}s.`, 'success');
            });
        }

        this.bindConfigTransfer();
    }

    // ========================================
    // Configuration export and import
    // ========================================

    // The endpoints have existed since the CLI was built; this is the same two
    // operations offered where an operator is already looking. The full backup
    // is deliberately not here — it stops the container serving this page.

    bindConfigTransfer() {
        const exportBtn = document.getElementById('exportConfigBtn');
        if (exportBtn) {
            exportBtn.addEventListener('click', () => this.exportConfiguration(exportBtn));
        }

        const importBtn = document.getElementById('importConfigBtn');
        const file = document.getElementById('importConfigFile');
        if (importBtn && file) {
            importBtn.addEventListener('click', () => file.click());
            file.addEventListener('change', () => {
                const chosen = file.files && file.files[0];
                // The picker is reset first: choosing the same file twice in a
                // row fires no change event otherwise, so a retry after a
                // failure would appear to do nothing.
                file.value = '';
                if (chosen) this.importConfiguration(chosen, importBtn);
            });
        }
    }

    // Sizes are quoted so an export that came back suspiciously small is
    // visible as such. Binary units, matching what the backup tool reports.
    formatBytes(bytes) {
        if (!Number.isFinite(bytes) || bytes < 0) return 'unknown size';
        if (bytes < 1024) return `${bytes} B`;

        const units = ['KB', 'MB', 'GB'];
        let value = bytes / 1024;
        let unit = 0;
        while (value >= 1024 && unit < units.length - 1) {
            value /= 1024;
            unit++;
        }
        return `${value.toFixed(1)} ${units[unit]}`;
    }

    configTransferStatus(message, tone) {
        const status = document.getElementById('configTransferStatus');
        if (!status) return;
        status.textContent = message;
        status.className = `settings-status${tone ? ` is-${tone}` : ''}`;
    }

    // The download is driven from fetch rather than by pointing the browser at
    // the endpoint, so a failure is reported on this page instead of replacing
    // it with the server's error text.
    async exportConfiguration(button) {
        button.disabled = true;
        this.configTransferStatus('Building the export…', null);

        try {
            const response = await fetch('/api/config/export');
            if (!response.ok) throw new Error(`HTTP ${response.status}`);

            const blob = await response.blob();
            // The server names the file with the instant it was taken; keeping
            // that name is what makes two exports distinguishable on disk.
            const disposition = response.headers.get('Content-Disposition') || '';
            const match = /filename="?([^"]+)"?/.exec(disposition);
            const name = match ? match[1] : 'netmon-config.tar.gz';

            const url = URL.createObjectURL(blob);
            const link = document.createElement('a');
            link.href = url;
            link.download = name;
            document.body.appendChild(link);
            link.click();
            link.remove();
            URL.revokeObjectURL(url);

            this.configTransferStatus(
                `Exported ${name} (${this.formatBytes(blob.size)}). It contains SNMP community strings — store it as a credential.`,
                'ok');
        } catch (error) {
            this.configTransferStatus(`Export failed: ${error.message}`, 'error');
            this.showNotification('Configuration export failed', 'error');
        } finally {
            button.disabled = false;
        }
    }

    // An import runs twice: a dry run to plan, then ?apply=true.
    async importConfiguration(file, button) {
        button.disabled = true;
        this.configTransferStatus(`Checking ${file.name}…`, null);

        try {
            const plan = await this.postConfigBundle(file, false);

            // report.changed counts devices and profiles; missing modules are still work.
            const newMIBs = (plan.counts || {}).mibs_imported || 0;
            if (!plan.changed && newMIBs === 0) {
                this.configTransferStatus(
                    `${file.name} matches the configuration already here. Nothing to import.`, 'ok');
                return;
            }

            if (!window.confirm(`Import ${file.name}?\n\n${this.describeImportPlan(plan)}\n\n` +
                    'Metric history is not affected. The configuration being replaced is ' +
                    'exported first, so a wrong import can be undone.')) {
                this.configTransferStatus('Import cancelled. Nothing was changed.', null);
                return;
            }

            this.configTransferStatus(`Importing ${file.name}…`, null);
            const applied = await this.postConfigBundle(file, true);

            const undo = applied.safety_export
                ? ` The previous configuration was saved to ${applied.safety_export}.`
                : '';
            this.configTransferStatus(
                `Imported ${file.name}. ${this.describeImportPlan(applied)}${undo}`, 'ok');
            this.showNotification('Configuration imported', 'success');

            // Tear the dashboard down first and reload everything from the imported configuration.
            const wasShowing = this.currentDashboardDevice;
            if (wasShowing) this.unsubscribeFromAllDashboards(wasShowing);
            this.dashboardLoad++;
            this.currentDashboardDevice = null;
            this.hideDeviceStatusBanner();

            const select = document.getElementById('dashboardDeviceSelect');
            try {
                await this.loadDevices({ restore: false, strict: true });
                await this.loadProfiles({ strict: true });
                await this.loadStoredMIBs({ strict: true });
            } catch (error) {
                // The import was applied but the page could not be rebuilt.
                this.configTransferStatus(
                    `Imported ${file.name}, but this page could not be refreshed ` +
                    `(${error.message}). Reload to see the new configuration.`, 'error');
                if (select) select.value = '';
                this.currentDashboardDevice = null;
                this.showFleetOverview();
                this.saveState();
                return;
            }

            const survived = wasShowing && select &&
                select.querySelector(`option[value="${wasShowing}"]`);
            if (survived) {
                this.openFleetDevice(wasShowing);
            } else {
                if (select) select.value = '';
                this.currentDashboardDevice = null;
                this.showFleetOverview();
            }
            this.saveState();
        } catch (error) {
            this.configTransferStatus(`Import failed: ${error.message}`, 'error');
            this.showNotification('Configuration import failed', 'error');
        } finally {
            button.disabled = false;
        }
    }

    // postConfigBundle sends the file, planning or applying. The server reports
    // its refusals as JSON with an error field, so that is preferred over the
    // status code when there is one to quote.
    async postConfigBundle(file, apply) {
        const body = new FormData();
        body.append('file', file);

        const response = await fetch(`/api/config/import?apply=${apply}`, { method: 'POST', body });
        const text = await response.text();

        let payload = null;
        try {
            payload = JSON.parse(text);
        } catch (error) {
            payload = null;
        }

        if (!response.ok) {
            const reason = payload && (payload.error || payload.message);
            throw new Error(reason || text.trim() || `HTTP ${response.status}`);
        }
        if (!payload) throw new Error('the server did not answer with a report');
        return payload;
    }

    // describeImportPlan turns the report into the sentence the operator has to
    // read before agreeing. Only what actually moves is mentioned, so a bundle
    // that touches one device does not present a wall of zeroes.
    describeImportPlan(report) {
        const counts = report.counts || {};
        const parts = [];

        const add = (n, singular, plural) => {
            if (n > 0) parts.push(`${n.toLocaleString()} ${n === 1 ? singular : plural}`);
        };
        add(counts.devices_created, 'device added', 'devices added');
        add(counts.devices_updated, 'device changed', 'devices changed');
        add(counts.devices_deleted, 'device removed', 'devices removed');
        add(counts.profiles_created, 'profile added', 'profiles added');
        add(counts.profiles_updated, 'profile changed', 'profiles changed');
        add(counts.profiles_deleted, 'profile removed', 'profiles removed');
        add(counts.mibs_imported, 'MIB imported', 'MIBs imported');
        add(counts.mibs_refreshed, 'MIB refreshed', 'MIBs refreshed');

        if (parts.length === 0) return 'No devices or profiles change.';
        return parts.join(', ') + '.';
    }

    // ========================================
    // Dashboard Navigation & Subscription Management
    // ========================================

    async fetchAndDisplayDeviceStatus(deviceId, load = this.dashboardLoad) {
        const banner = document.getElementById('deviceStatusBanner');

        try {
            const response = await fetch(`/api/devices/${deviceId}/status`);
            // The banner is shared, so check this is still the current device first.
            if (load !== this.dashboardLoad) return;

            if (!response.ok) {
                // Handle different error cases
                if (response.status === 404) {
                    // Device not registered with poller yet - this is normal for new devices
                    // Don't show a warning, just hide the banner
                    // Status tracking will be available once the poller processes the device
                    banner.style.display = 'none';
                    return;
                } else if (response.status === 503) {
                    // Only show warning if poller is down
                    this.showDeviceStatusBanner('warning', 'Poller Unavailable',
                        'The poller service is currently unavailable. Please check if the go-netmon service is running.',
                        null);
                    return;
                }
                // Hide banner for other errors (don't show confusing messages)
                banner.style.display = 'none';
                return;
            }

            const status = await response.json();
            if (load !== this.dashboardLoad) return;

            // Check for errors in the status
            if (status.last_error && status.last_error !== '') {
                // There's an error - show error banner
                const lastPollTime = status.last_poll_time ? new Date(status.last_poll_time).toLocaleString() : 'Never';
                const details = {
                    'Last Poll': lastPollTime,
                    'Metrics Collected': status.metrics_collected || 0,
                    'Poll Interval': status.poll_interval || 'Unknown'
                };

                this.showDeviceStatusBanner('error', 'Polling Error', status.last_error, details);
            } else if (!status.connected) {
                // Device not connected
                const lastPollTime = status.last_poll_time ? new Date(status.last_poll_time).toLocaleString() : 'Never';
                this.showDeviceStatusBanner('warning', 'Device Disconnected',
                    'Unable to connect to this device. Please verify the device is online and SNMP is properly configured.',
                    { 'Last Successful Poll': lastPollTime });
            } else if (!status.last_poll_success) {
                // Last poll failed but device is connected
                const lastPollTime = status.last_poll_time ? new Date(status.last_poll_time).toLocaleString() : 'Never';
                this.showDeviceStatusBanner('warning', 'Last Poll Failed',
                    'The most recent polling attempt was unsuccessful. The next poll may succeed.',
                    { 'Last Poll Attempt': lastPollTime });
            } else {
                // Everything is OK - hide the banner
                banner.style.display = 'none';
            }
        } catch (error) {
            console.error('Error fetching device status:', error);
            // A rejection is a completion path like any other. Hiding the
            // banner unconditionally let a departed device's failed request
            // clear the error banner belonging to the device on screen.
            if (load !== this.dashboardLoad) return;
            // Don't show an error banner for fetch failures - just log it
            banner.style.display = 'none';
        }
    }

    showDeviceStatusBanner(type, title, message, details) {
        const banner = document.getElementById('deviceStatusBanner');

        // Determine icon based on type
        let icon = '';
        switch (type) {
            case 'error':
                icon = '\u26A0\uFE0F'; // Warning sign emoji
                break;
            case 'warning':
                icon = '\u26A0\uFE0F'; // Warning sign emoji
                break;
            case 'success':
                icon = '\u2705'; // Check mark emoji
                break;
            default:
                icon = '\u2139\uFE0F'; // Info emoji
        }

        // Build details HTML if provided
        let detailsHtml = '';
        if (details) {
            detailsHtml = '<div class="status-details">';
            for (const [label, value] of Object.entries(details)) {
                detailsHtml += `
                    <div class="status-detail-item">
                        <span class="status-detail-label">${label}</span>
                        <span class="status-detail-value">${value}</span>
                    </div>
                `;
            }
            detailsHtml += '</div>';
        }

        banner.className = `device-status-banner ${type}`;
        banner.innerHTML = `
            <span class="status-icon">${icon}</span>
            <div class="status-content">
                <div class="status-title">${title}</div>
                <div class="status-message">${message}</div>
                ${detailsHtml}
            </div>
            <button class="dismiss-btn" onclick="document.getElementById('deviceStatusBanner').style.display='none'" title="Dismiss">&times;</button>
        `;
        banner.style.display = 'flex';
    }

    hideDeviceStatusBanner() {
        const banner = document.getElementById('deviceStatusBanner');
        banner.style.display = 'none';
    }

    // One request per device returns every panel, grouped here by module.
    async loadDeviceDashboards(deviceId) {
        const load = ++this.dashboardLoad;
        try {
            await this.fetchAndDisplayDeviceStatus(deviceId, load);
            if (load !== this.dashboardLoad) return;

            const response = await fetch(`/api/devices/${deviceId}/dashboard`);
            if (load !== this.dashboardLoad) return;
            if (!response.ok) {
                document.getElementById('dashboardContent').innerHTML = `
                    <div class="empty-state">
                        <h3>No MIBs Assigned</h3>
                        <p>This device has no MIBs assigned. Go to the device details page to assign MIBs.</p>
                    </div>
                `;
                document.getElementById('mibTabsContainer').style.display = 'none';
                return;
            }

            const dashboard = await response.json();
            if (load !== this.dashboardLoad) return;

            const byModule = this.groupPanelsByModule(dashboard);

            // The rail lists the modules that contributed something. A module
            // reporting nothing still has a coverage row explaining why, but a
            // tab with no panels in it is a tab nobody wants to click.
            const names = [...byModule.keys()].sort((a, b) => {
                // The device's own metrics first: they describe the box rather
                // than one of its MIBs.
                if (a === UNATTRIBUTED_MODULE) return -1;
                if (b === UNATTRIBUTED_MODULE) return 1;
                return a.localeCompare(b);
            });

            if (names.length === 0) {
                document.getElementById('dashboardContent').innerHTML = `
                    <div class="empty-state">
                        <h3>Nothing reported yet</h3>
                        <p>${this.escapeHtml(dashboard.warnings?.[0] ||
                            'This device is not reporting any metrics.')}</p>
                    </div>
                `;
                document.getElementById('mibTabsContainer').style.display = 'none';
                return;
            }

            this.deviceDashboards[deviceId] = {
                mibs: names.map(name => ({ name })),
                dashboards: byModule
            };

            document.getElementById('dashboardContent').style.display = 'none';
            document.getElementById('mibTabsContainer').style.display = 'block';

            // Reopen the tab the reader left on, if the device still has it.
            this.activeMibTab = names.includes(this.restoredMibTab)
                ? this.restoredMibTab : names[0];
            this.restoredMibTab = null;

            this.createMibTabs(deviceId, this.deviceDashboards[deviceId].mibs);
            // Render and subscribe only the landing tab; others load on first activation.
            this.ensureMibTabLoaded(deviceId, this.activeMibTab, load);
        } catch (error) {
            console.error('Error loading device dashboards:', error);
            // The reader is looking at something else; a failure notice about
            // the device they left reads as a failure of the one they opened.
            if (load !== this.dashboardLoad) return;
            this.showNotification('Failed to load device dashboards', 'error');
        }
    }

    // groupPanelsByModule splits a device's panels by module; module-less panels get a device entry; an unreadable catalog keeps them whole.
    groupPanelsByModule(dashboard) {
        const byModule = new Map();
        const noteFor = name => (dashboard.mibs || [])
            .filter(c => c.mib === name).map(c => c.note).filter(Boolean);

        const put = (name, panel) => {
            let held = byModule.get(name);
            if (!held) {
                held = {
                    mib: name,
                    panels: [],
                    warnings: noteFor(name),
                    // Carry this module's tables through the split.
                    tables: (dashboard.tables || []).filter(table =>
                        (table.module || UNATTRIBUTED_MODULE) === name)
                };
                byModule.set(name, held);
            }
            held.panels.push(panel);
        };

        for (const panel of dashboard.panels || []) {
            put(dashboard.catalog_unavailable
                ? UNREADABLE_MODULE
                : (panel.module || UNATTRIBUTED_MODULE), panel);
        }

        // A read failure explains every tab, so it goes on the one tab there is.
        if (dashboard.catalog_unavailable && byModule.has(UNREADABLE_MODULE)) {
            byModule.get(UNREADABLE_MODULE).warnings = dashboard.warnings || [];
        }
        return byModule;
    }

    // createMibTabs builds a filterable module rail and one pane per module, in the server's order.
    createMibTabs(deviceId, mibs) {
        const tabList = document.getElementById('mibTabList');
        const tabContent = document.getElementById('mibTabContent');

        // A rebuild drops every pane: release this device's subscriptions and loaded-tab marks.
        this.unsubscribeFromAllDashboards(deviceId);
        this.loadedMibTabs = new Set();

        // Clear existing tabs
        tabList.innerHTML = '';
        tabContent.innerHTML = '';

        mibs.forEach((mib, index) => {
            const active = mib.name === this.activeMibTab ||
                (!this.activeMibTab && index === 0);
            tabList.appendChild(this.mibTabButton(deviceId, mib.name, active));

            const tabPane = document.createElement('div');
            tabPane.className = 'tab-pane' + (active ? ' active' : '');
            tabPane.id = `mib-tab-${mib.name}`;
            tabPane.setAttribute('role', 'tabpanel');
            tabPane.setAttribute('aria-labelledby', this.mibTabButtonId(mib.name));
            tabPane.tabIndex = 0;
            tabPane.innerHTML = '<div class="charts-loading">Initializing...</div>';
            tabContent.appendChild(tabPane);
        });

        this.bindMibTabNav();

        // A new device starts from an unfiltered rail; the previous device's
        // search term has nothing to say about this one's modules.
        const search = document.getElementById('mibTabSearch');
        if (search) search.value = '';
        this.filterMibTabs('');
    }

    mibTabButtonId(mibName) {
        return `mib-tab-btn-${mibName}`;
    }

    // One row of the rail. It keeps the class .tab-button and nothing but the
    // module name as its text, because both the dashboard specs and the
    // empty-module sweep identify a module that way.
    mibTabButton(deviceId, mibName, active) {
        const button = document.createElement('button');
        button.type = 'button';
        button.id = this.mibTabButtonId(mibName);
        button.className = 'tab-button' + (active ? ' active' : '');
        button.setAttribute('role', 'tab');
        button.setAttribute('aria-selected', active ? 'true' : 'false');
        button.setAttribute('aria-controls', `mib-tab-${mibName}`);
        // Roving tabindex: Tab reaches the rail once and lands on the selected
        // module, and the arrow keys move within it.
        button.tabIndex = active ? 0 : -1;
        button.dataset.mib = mibName;
        button.dataset.mibSearch = mibName.toLowerCase();
        button.textContent = mibName;
        button.onclick = () => this.switchMibTab(deviceId, mibName);
        return button;
    }

    bindMibTabNav() {
        if (this.mibTabNavBound) return;

        const tabList = document.getElementById('mibTabList');
        const search = document.getElementById('mibTabSearch');
        if (!tabList || !search) return;

        search.addEventListener('input', () => this.filterMibTabs(search.value));
        search.addEventListener('keydown', (event) => {
            if (event.key === 'ArrowDown') {
                event.preventDefault();
                this.focusMibTab(0);
            } else if (event.key === 'Enter') {
                // Typing a name and pressing Enter is the fast path: the first
                // remaining module is almost always the one that was meant.
                event.preventDefault();
                const first = this.selectableMibTabs()[0];
                if (first) first.click();
            } else if (event.key === 'Escape' && search.value !== '') {
                event.preventDefault();
                search.value = '';
                this.filterMibTabs('');
            }
        });

        tabList.addEventListener('keydown', (event) => this.handleMibTabKey(event));
        this.mibTabNavBound = true;
    }

    // filterMibTabs narrows the rail without changing the selection.
    filterMibTabs(searchTerm) {
        const tabList = document.getElementById('mibTabList');
        if (!tabList) return;

        const term = (searchTerm || '').toLowerCase().trim();
        let total = 0;
        let matched = 0;

        tabList.querySelectorAll('.tab-button').forEach(button => {
            // A module the sweep retired for having no data is not in the list
            // at all, so it is not part of the counts either.
            if (button.classList.contains('no-data-hidden')) return;
            total++;
            const hit = term === '' || (button.dataset.mibSearch || '').includes(term);
            button.classList.toggle('is-filtered-out', !hit);
            if (hit) matched++;
        });

        const count = document.getElementById('mibTabCount');
        if (count) {
            count.textContent = term
                ? `${matched.toLocaleString()} of ${total.toLocaleString()} modules match “${term}”`
                : `${total.toLocaleString()} module${total === 1 ? '' : 's'}`;
        }

        const none = document.getElementById('mibTabNoMatch');
        if (none) none.hidden = matched > 0 || total === 0;
    }

    // selectableMibTabs is the rail as the keyboard sees it: whatever the
    // filter left, minus the modules the empty-module sweep retired.
    selectableMibTabs() {
        const tabList = document.getElementById('mibTabList');
        if (!tabList) return [];
        return Array.from(tabList.querySelectorAll(
            '.tab-button:not(.is-filtered-out):not(.no-data-hidden)'));
    }

    focusMibTab(index) {
        const tabs = this.selectableMibTabs();
        if (tabs.length === 0) return;
        const target = tabs[Math.max(0, Math.min(index, tabs.length - 1))];
        target.focus();
        target.scrollIntoView({ block: 'nearest' });
    }

    // Arrow keys move focus along the rail; Enter or Space opens the module.
    handleMibTabKey(event) {
        const tabs = this.selectableMibTabs();
        const current = tabs.indexOf(document.activeElement);
        if (current === -1) return;

        const moves = {
            ArrowDown: current + 1,
            ArrowUp: current - 1,
            Home: 0,
            End: tabs.length - 1
        };
        if (!(event.key in moves)) return;

        event.preventDefault();
        this.focusMibTab((moves[event.key] + tabs.length) % tabs.length);
    }

    switchMibTab(deviceId, mibName) {
        console.log(`Switching to MIB tab: ${mibName} for device: ${deviceId}`);

        // Update active tab button
        const tabButtons = document.querySelectorAll('#mibTabList .tab-button');
        tabButtons.forEach(btn => {
            const selected = (btn.dataset.mib || btn.textContent) === mibName;
            btn.classList.toggle('active', selected);
            btn.setAttribute('aria-selected', selected ? 'true' : 'false');
            btn.tabIndex = selected ? 0 : -1;
            if (selected) btn.scrollIntoView({ block: 'nearest' });
        });

        // Update active tab pane
        const tabPanes = document.querySelectorAll('.tab-pane');
        tabPanes.forEach(pane => {
            if (pane.id === `mib-tab-${mibName}`) {
                pane.classList.add('active');
            } else {
                pane.classList.remove('active');
            }
        });

        // Store active tab and save state
        this.activeMibTab = mibName;
        this.saveState();

        // First time this tab is shown, render and subscribe it. An
        // already-loaded tab just became visible; its subscription was never
        // dropped, so there is nothing to do but resize its charts below.
        this.ensureMibTabLoaded(deviceId, mibName);

        // Resize the newly visible tab's charts, retrying with increasing delays for layout.
        this.resizeChartsInTab(mibName);
    }

    resizeChartsInTab(mibName) {
        const activeTabPane = document.getElementById(`mib-tab-${mibName}`);
        if (!activeTabPane) return;

        const doResize = () => {
            console.log(`Resizing charts for MIB tab: ${mibName}`);
            // Select charts by class.
            const chartElements = activeTabPane.querySelectorAll('.chart-canvas');
            console.log(`Found ${chartElements.length} chart elements to resize`);

            chartElements.forEach(chartElement => {
                const chartId = chartElement.id;
                const chartInstance = this.charts[chartId];

                if (chartInstance && typeof chartInstance.resize === 'function') {
                    try {
                        // The canvas element carries the authoritative size; an
                        // imposed height would spill the plot out of its card.
                        chartInstance.resize(AUTO_SIZE);
                        console.log(`Resized chart: ${chartId}`);
                    } catch (error) {
                        console.error(`Error resizing chart ${chartId}:`, error);
                    }
                } else {
                    console.warn(`Chart instance not found or resize not available for: ${chartId}`);
                }
            });
        };

        // Multiple resize attempts to handle various layout timing scenarios
        setTimeout(doResize, 50);
        setTimeout(doResize, 150);
        setTimeout(doResize, 300);
        setTimeout(doResize, 500);
    }

    // Dashboard rendering from DashboardSpec.

    // ensureMibTabLoaded renders and subscribes a tab once; a failed render releases the key so the tab can retry.
    ensureMibTabLoaded(deviceId, mibName, load = this.dashboardLoad) {
        if (!mibName) return;
        const key = `${deviceId}-${mibName}`;
        if (this.loadedMibTabs.has(key)) return;
        this.loadedMibTabs.add(key);
        this.loadMibCharts(deviceId, mibName, load).catch(error => {
            console.error(`Error loading MIB tab ${mibName}:`, error);

            // Both belong to the failed load, so gate them on it still being current.
            if (load !== this.dashboardLoad) return;
            this.loadedMibTabs.delete(key);

            // Show the failure in the pane, not only in a toast.
            const pane = document.getElementById(`mib-tab-${mibName}`);
            if (pane) {
                pane.innerHTML = `<div class="empty-state">
                    <h3>Could not load ${this.escapeHtml(mibName)}</h3>
                    <p>Select the module again to retry.</p>
                </div>`;
            }
            this.showNotification(`Failed to load ${mibName}`, 'error');
        });
    }

    // Render one module's tab from the device answer already held.
    async loadMibCharts(deviceId, mibName, load = this.dashboardLoad) {
        const tabPane = document.getElementById(`mib-tab-${mibName}`);
        if (!tabPane) return;

        const deviceDash = this.deviceDashboards[deviceId];
        const held = deviceDash && deviceDash.dashboards.get(mibName);
        if (!held) {
            tabPane.innerHTML = '<div class="empty-state"><p>Nothing here for this device.</p></div>';
            return;
        }

        await this.renderPanels(deviceId, mibName, held, load);
    }

    // One metric card; its head shows provenance, name, unit and description on independent size, weight, colour and case.
    panelHeadMarkup(panel, options = {}) {
        const provenance = panel.module
            || (panel.kind === 'string' && !panel.object ? 'Reading' : '')
            || 'Unattributed';

        // The MIB object name where there is one, else the published name without its snmp_/_info affixes, in mono.
        const named = Boolean(panel.object);
        const name = panel.object || this.publishedName(panel.metric);

        const unit = this.panelUnit(panel);

        // Scalar tiles get a compact description control; chart cards keep the clipped line.
        const compact = Boolean(options.compactDescription);
        const descTrigger = this.descriptionTriggerHtml(panel.description, {
            title: name,
            eyebrow: panel.module || 'Metric',
            className: compact ? 'panel-note-compact' : 'panel-note',
            compact,
            meta: [['Metric', panel.metric], ['OID', panel.oid]]
        });

        return `
            <header class="panel-head${compact ? ' panel-head-compact' : ''}">
                <p class="panel-eyebrow">${this.escapeHtml(provenance)}</p>
                <div class="panel-headline">
                    <h4 class="panel-title${named ? '' : ' is-identifier'}"
                        title="${this.escapeAttr(name)}">${this.escapeHtml(name)}</h4>
                    ${unit ? `<span class="panel-unit">${this.escapeHtml(unit)}</span>` : ''}
                </div>
                ${descTrigger}
            </header>
        `;
    }

    // One metric card. The plot is the card; the head above says what it is.
    chartCardMarkup(chartId, panel) {
        return `
            <article class="panel chart-container is-empty">
                ${this.panelHeadMarkup(panel)}
                <div id="${chartId}" class="chart-canvas"></div>
                <div class="chart-stale" hidden>
                    <span class="chart-stale-text"></span>
                </div>
                <div class="chart-empty">
                    <span class="chart-empty-title">Waiting for data…</span>
                    <span class="chart-empty-detail">Listening for the first sample.</span>
                </div>
            </article>
        `;
    }

    // A tile is the same head over a reading that is words rather than a plot --
    // but the reading is the content here, so its head keeps the description as
    // a compact control rather than a clipped paragraph above the value.
    tileCardMarkup(elementId, panel) {
        return `
            <article class="panel status-item">
                ${this.panelHeadMarkup(panel, { compactDescription: true })}
                <div id="${elementId}" class="status-value" data-waiting="1">Waiting for data…</div>
            </article>
        `;
    }

    async renderPanels(deviceId, mibName, dashboard, load = this.dashboardLoad) {
        const tabPane = document.getElementById(`mib-tab-${mibName}`);
        if (!tabPane) return;

        const panels = dashboard.panels || [];

        // State what the dashboard could not explain, before the empty case.
        let html = '';
        for (const warning of dashboard.warnings || []) {
            html += `<div class="dashboard-warning">${this.escapeHtml(warning)}</div>`;
        }

        if (panels.length === 0) {
            tabPane.innerHTML = html + `
                <div class="empty-state">
                    <p>${this.escapeHtml(mibName)} is not reporting anything on this device.</p>
                </div>
            `;
            return;
        }

        // Readings that are words, and states that are names, are tiles rather
        // than plots: a line through a constant 1 says nothing.
        const tiles = panels.filter(p => p.type === 'state' || p.type === 'text');
        const plots = panels.filter(p => p.type !== 'state' && p.type !== 'text');

        // Two sections, each announced. A heading with a rule under it is what
        // tells the eye where one ends and the next begins; without them the
        // page is an undifferentiated field of cards.
        if (tiles.length > 0) {
            html += this.sectionHeadMarkup('States and readings', tiles.length,
                'Values that are words rather than numbers, so they are listed rather than plotted.');
            html += '<div class="status-grid">';
            for (const panel of tiles) {
                html += this.tileCardMarkup(
                    this.panelElementId(deviceId, mibName, panel), panel);
            }
            html += '</div>';
        }

        // The rows come first, because they are what a reader is looking for:
        // which interface, and what does it say. The charts under them answer
        // "and what has it been doing", for the rows picked here.
        const tables = dashboard.tables || [];
        for (const table of tables) {
            html += this.rowTableMarkup(deviceId, mibName, table, panels);
        }

        if (plots.length > 0) {
            html += this.sectionHeadMarkup('Measurements', plots.length,
                'Numbers over time. A counter is drawn as its rate.');
            html += '<div class="charts-grid">';
            for (const panel of plots) {
                html += this.chartCardMarkup(
                    this.panelElementId(deviceId, mibName, panel), panel);
            }
            html += '</div>';
        }

        // Dispose the tab's ECharts instances before replacing its markup.
        for (const canvas of tabPane.querySelectorAll('.chart-canvas')) {
            this.discardChart(canvas.id);
        }

        tabPane.innerHTML = html;
        this.paneTables.set(`${deviceId}-${mibName}`, { tables, panels });

        // Visibility belongs to .tab-pane.active; resizeChartsInTab sizes charts when shown.
        void tabPane.offsetHeight;
        await new Promise(resolve => setTimeout(resolve, 100));

        // Stop if the reader left during the pause, before installing timers and subscriptions.
        if (load !== this.dashboardLoad) return;

        // A tile that never receives a sample has to say so on the same
        // deadline a chart card uses, or it sits on "Waiting for data" for the
        // rest of the session and reads as slow rather than as silent.
        for (const panel of tiles) {
            const elementId = this.panelElementId(deviceId, mibName, panel);
            setTimeout(() => this.checkPanelTileTimeout(elementId), FIRST_SAMPLE_TIMEOUT);
        }

        this.subscribeToPanels(deviceId, mibName, panels);
        this.resizeChartsInTab(mibName);

        setTimeout(() => {
            this.checkAndHideEmptyMibTab(deviceId, mibName);
        }, 35000);
    }

    // panelUnit is the panel's unit; a counter is drawn as its rate, per second.
    panelUnit(panel) {
        if (panel.type !== 'rate') return panel.unit || '';
        return panel.unit ? `${panel.unit}/s` : 'per second';
    }

    // publishedName strips the snmp_ and _info affixes from a metric name.
    publishedName(metric) {
        let name = String(metric || '');
        if (name.startsWith('snmp_')) name = name.slice(5);
        if (name.endsWith('_info')) name = name.slice(0, -5);
        return name || String(metric || '');
    }

    // sectionHeadMarkup announces a group of panels with its count.
    sectionHeadMarkup(title, count, note) {
        return `
            <div class="panel-section">
                <h3 class="panel-section-title">${this.escapeHtml(title)}</h3>
                <span class="panel-section-count">${count}</span>
                <p class="panel-section-note">${this.escapeHtml(note)}</p>
            </div>
        `;
    }

    // panelElementId is the DOM id of a panel's tile or chart, from the panel id.
    panelElementId(deviceId, mibName, panel) {
        return `panel-${deviceId}-${mibName}-${this.slug(panel.id)}`;
    }

    // Row tables: indexed metrics list current values per row; charts draw only the rows the reader picks.

    tableKey(deviceId, mibName, table) {
        return `${deviceId}-${mibName}-${this.slug(table.id)}`;
    }

    // tableColumns are the columns shown by default: the server decided which,
    // from the catalog's kinds and from what the rows currently read.
    tableColumns(table, panels) {
        const byMetric = new Map(panels.map(panel => [panel.metric, panel]));
        return (table.columns || [])
            .filter(column => column.default)
            .map(column => ({ column, panel: byMetric.get(column.metric) }))
            .filter(entry => entry.panel);
    }

    // tableSummary states the columns every row agrees about.
    tableSummary(table, panels) {
        const byMetric = new Map(panels.map(panel => [panel.metric, panel]));
        return (table.columns || [])
            .filter(column => !column.varies && column.value !== undefined && column.value !== '')
            .map(column => {
                const panel = byMetric.get(column.metric);
                const name = panel ? (panel.object || panel.metric) : column.metric;
                return `${name} ${column.value}`;
            });
    }

    rowTableMarkup(deviceId, mibName, table, panels) {
        const key = this.tableKey(deviceId, mibName, table);
        // A table with no default columns still gets a body so rows can be picked.
        const columns = this.tableColumns(table, panels);

        const summary = this.tableSummary(table, panels);
        const heads = columns.map(entry =>
            `<th scope="col">${this.escapeHtml(entry.panel.object || entry.panel.metric)}</th>`).join('');

        return `
            <section class="row-table" data-table-key="${this.escapeAttr(key)}">
                <div class="row-table-controls">
                    <label class="row-table-filter">
                        <span class="visually-hidden">Filter rows</span>
                        <input type="search" class="row-table-search"
                               placeholder="Filter rows…"
                               data-table-key="${this.escapeAttr(key)}">
                    </label>
                    <span class="row-table-count" aria-live="polite"></span>
                </div>
                ${summary.length > 0
                    ? `<p class="row-table-summary">All rows: ${summary.map(s => this.escapeHtml(s)).join(' · ')}</p>`
                    : ''}
                <table class="row-table-grid">
                    <thead>
                        <tr>
                            <th scope="col" class="row-table-pick">Chart</th>
                            <th scope="col">Row</th>
                            ${heads}
                        </tr>
                    </thead>
                    <tbody data-table-body="${this.escapeAttr(key)}">
                        <tr class="row-table-waiting"><td colspan="${columns.length + 2}">Waiting for the first reading…</td></tr>
                    </tbody>
                </table>
            </section>
        `;
    }

    // recordTableRow notes what one row of one table currently reads.
    recordTableRow(key, rowKey, name, metric, text) {
        let rows = this.tableRows.get(key);
        if (!rows) {
            rows = new Map();
            this.tableRows.set(key, rows);
        }
        let row = rows.get(rowKey);
        if (!row) {
            row = { name, values: new Map() };
            rows.set(rowKey, row);
        }
        if (name) row.name = name;
        row.values.set(metric, text);
    }

    // selectionFor returns the rows charted for a table.
    selectionFor(key) {
        return this.tableSelection.get(key) || new Set();
    }

    // seedSelection applies the default once, the first time a table has rows.
    seedSelection(key, ordered) {
        if (this.tableSeeded.has(key) || ordered.length === 0) return;
        this.tableSeeded.add(key);
        this.tableSelection.set(key, new Set(ordered.slice(0, DEFAULT_SELECTED_ROWS)));
    }

    // orderedRowKeys is the table's row order, by displayed name with numeric runs compared numerically.
    orderedRowKeys(rows) {
        return [...rows.keys()].sort((a, b) =>
            String(rows.get(a).name).localeCompare(String(rows.get(b).name), undefined,
                { numeric: true, sensitivity: 'base' }));
    }

    // renderTableBody draws the rows, in name order, filtered by the search box.
    renderTableBody(deviceId, mibName, table, panels) {
        const key = this.tableKey(deviceId, mibName, table);
        const body = document.querySelector(`[data-table-body="${CSS.escape(key)}"]`);
        if (!body) return;

        const columns = this.tableColumns(table, panels);
        const rows = this.tableRows.get(key) || new Map();
        const ordered = this.orderedRowKeys(rows);
        const selected = this.selectionFor(key);

        const needle = (this.tableFilter.get(key) || '').toLowerCase();
        const visible = ordered.filter(rowKey =>
            needle === '' || String(rows.get(rowKey).name).toLowerCase().includes(needle));

        const count = document.querySelector(
            `[data-table-key="${CSS.escape(key)}"] .row-table-count`);
        if (count) {
            const scope = needle
                ? `${visible.length} of ${ordered.length} rows`
                : `${ordered.length} rows`;
            // An empty selection is a choice, and the charts below are empty
            // because of it. Saying "0 charted" alone reads as a fault; saying
            // what to do about it does not.
            count.textContent = selected.size === 0
                ? `${scope} · none charted — tick a row to chart it`
                : `${scope} · ${selected.size} charted`;
        }

        if (visible.length === 0) {
            body.innerHTML = `<tr class="row-table-waiting"><td colspan="${columns.length + 2}">${
                ordered.length === 0 ? 'Waiting for the first reading…' : 'No row matches that filter.'
            }</td></tr>`;
            return;
        }

        body.innerHTML = visible.map(rowKey => {
            const row = rows.get(rowKey);
            const picked = selected.has(rowKey);
            const cells = columns.map(entry =>
                `<td>${this.escapeHtml(row.values.get(entry.column.metric) ?? '—')}</td>`).join('');
            return `
                <tr class="${picked ? 'is-charted' : ''}">
                    <td class="row-table-pick">
                        <input type="checkbox" ${picked ? 'checked' : ''}
                               data-table-key="${this.escapeAttr(key)}"
                               data-row-key="${this.escapeAttr(rowKey)}"
                               aria-label="Chart ${this.escapeAttr(row.name)}">
                    </td>
                    <th scope="row">${this.escapeHtml(row.name)}</th>
                    ${cells}
                </tr>
            `;
        }).join('');
    }

    // Search: match metric, object, row identity and friendly labels; matching rows are charted over the selected time range.

    bindSeriesSearch() {
        const input = document.getElementById('seriesSearch');
        if (!input) return;
        input.addEventListener('input', () => this.searchSeries(input.value.trim()));
    }

    // seriesMatches returns, per table, the row keys whose name or identity
    // matches -- and the panels whose own names match, which is how a reader
    // finds "temperature" rather than a port.
    seriesMatches(needle) {
        const lowered = needle.toLowerCase();
        const perTable = new Map();
        let panels = 0;

        for (const [paneKey, pane] of this.paneTables) {
            const [deviceId, ...rest] = paneKey.split('-');
            const mibName = rest.join('-');

            for (const panel of pane.panels) {
                const words = `${panel.metric} ${panel.object || ''} ${panel.title || ''}`;
                if (words.toLowerCase().includes(lowered)) panels++;
            }

            for (const table of pane.tables) {
                const key = this.tableKey(deviceId, mibName, table);
                const rows = this.tableRows.get(key);
                if (!rows) continue;
                const hits = [...rows.keys()].filter(rowKey => {
                    const row = rows.get(rowKey);
                    // Match the key, the name and the row's readings, which include aliases.
                    const words = [rowKey, row.name, ...row.values.values()].join(' ');
                    return words.toLowerCase().includes(lowered);
                });
                if (hits.length > 0) perTable.set(key, hits);
            }
        }
        return { perTable, panels };
    }

    // searchSeries charts what matches and reports how much; an empty box restores the default selection.
    searchSeries(needle) {
        const count = document.getElementById('seriesSearchCount');

        if (needle === '') {
            for (const key of this.tableSeeded) {
                const rows = this.tableRows.get(key) || new Map();
                this.tableSelection.set(key,
                    new Set(this.orderedRowKeys(rows).slice(0, DEFAULT_SELECTED_ROWS)));
                this.redrawTable(key);
                this.redrawChartsFor(key);
            }
            if (count) count.textContent = '';
            return;
        }

        const { perTable, panels } = this.seriesMatches(needle);
        let charted = 0;

        for (const [key, hits] of perTable) {
            // Bounded by the same ceiling a reader is held to by hand. A search
            // that matched four hundred ports and drew them all would rebuild
            // the hairball through a different door.
            const take = hits.slice(0, MAX_SELECTED_ROWS);
            charted += take.length;
            this.tableSelection.set(key, new Set(take));
            this.redrawTable(key);
            this.redrawChartsFor(key);
        }

        if (!count) return;
        const rows = [...perTable.values()].reduce((n, hits) => n + hits.length, 0);
        if (rows === 0 && panels === 0) {
            // Said explicitly. A blank count after a search reads as the search
            // not having run, which is the failure this whole codebase has
            // spent two days removing.
            count.textContent = `No reading matches “${needle}”.`;
            return;
        }
        const capped = rows > charted ? `, charting ${charted}` : '';
        count.textContent =
            `${rows} row${rows === 1 ? '' : 's'} and ${panels} metric${panels === 1 ? '' : 's'} match${capped}.`;
    }

    // bindRowTables wires the table's two controls once, on the document, so a
    // pane re-render does not have to re-attach anything.
    bindRowTables() {
        document.addEventListener('change', event => {
            const box = event.target.closest('input[type="checkbox"][data-row-key]');
            if (!box) return;
            this.toggleChartedRow(box.dataset.tableKey, box.dataset.rowKey, box.checked, box);
        });

        document.addEventListener('input', event => {
            const search = event.target.closest('.row-table-search');
            if (!search) return;
            this.tableFilter.set(search.dataset.tableKey, search.value.trim());
            this.redrawTable(search.dataset.tableKey);
        });
    }

    // toggleChartedRow adds or removes a charted row, bounded, and says when it refuses.
    toggleChartedRow(key, rowKey, wanted, box) {
        const selected = this.tableSelection.get(key) || new Set();
        if (wanted) {
            if (selected.size >= MAX_SELECTED_ROWS) {
                if (box) box.checked = false;
                this.showNotification(
                    `Charting is limited to ${MAX_SELECTED_ROWS} rows at once. Clear one to add another.`,
                    'warning');
                return;
            }
            selected.add(rowKey);
        } else {
            selected.delete(rowKey);
        }
        this.tableSelection.set(key, selected);
        this.redrawTable(key);
        this.redrawChartsFor(key);
    }

    redrawTable(key) {
        for (const [paneKey, pane] of this.paneTables) {
            const [deviceId, ...rest] = paneKey.split('-');
            const mibName = rest.join('-');
            for (const table of pane.tables) {
                if (this.tableKey(deviceId, mibName, table) === key) {
                    this.renderTableBody(deviceId, mibName, table, pane.panels);
                }
            }
        }
    }

    // redrawChartsFor repaints the charts of a table whose selection changed,
    // from data already held: picking a row shows its history immediately
    // rather than starting it from the next tick.
    redrawChartsFor(key) {
        for (const [paneKey, pane] of this.paneTables) {
            const [deviceId, ...rest] = paneKey.split('-');
            const mibName = rest.join('-');
            for (const table of pane.tables) {
                if (this.tableKey(deviceId, mibName, table) !== key) continue;
                for (const column of table.columns || []) {
                    const panel = pane.panels.find(p => p.metric === column.metric);
                    if (!panel) continue;
                    const chart = this.charts[this.panelElementId(deviceId, mibName, panel)];
                    if (chart && chart._panelSeries && chart._panelSeries.size > 0) {
                        chart._lastUpdate = 0;
                        chart.setOption(this.buildPanelOption(chart, panel), true);
                    }
                }
            }
        }
    }

    // slug reduces a panel id to something usable as a DOM id.
    slug(value) {
        return String(value).replace(/[^A-Za-z0-9_-]/g, '_');
    }

    // subscribeToPanels streams every panel of one module's tab with the server-built queries.
    subscribeToPanels(deviceId, mibName, panels) {
        // The tab's markup was replaced, so start its subscription sets afresh.
        this.unsubscribeFromDashboard(deviceId, mibName);

        const key = `${deviceId}-${mibName}`;
        const subscription = {
            metricQueries: new Set(),
            statusQueries: new Set(),
            objects: new Map()
        };

        for (const panel of panels) {
            const elementId = this.panelElementId(deviceId, mibName, panel);
            const target = { panel, elementId };

            if (panel.type === 'state' || panel.type === 'text') {
                subscription.statusQueries.add(panel.query);
            } else {
                subscription.metricQueries.add(panel.query);
                this.initializeEmptyChart(elementId, panel.object || panel.metric);
                // Backfill history when the panel is registered, without awaiting it.
                this.backfillPanel(target).catch(error =>
                    console.warn(`backfill for ${panel.title}:`, error));
            }
            subscription.objects.set(panel.query, target);
        }

        // Record the subscription only once every panel registered, just before sending.
        this.activeSubscriptions.set(key, subscription);

        const queries = [...subscription.statusQueries, ...subscription.metricQueries];
        if (queries.length > 0 && this.ws && this.ws.readyState === WebSocket.OPEN) {
            this.ws.send(JSON.stringify({ type: 'subscribe', queries, supports: WS_SUPPORTS }));
        }
    }

    // seriesName is a line's name: the first server-ranked name label the series carries, else its index labels.
    seriesName(panel, labels) {
        return this.seriesIdentity(panel, labels).name;
    }

    // seriesIdentity is seriesName plus whether it found a name rather than an index.
    seriesIdentity(panel, labels) {
        for (const label of panel.series_labels || []) {
            const value = labels[label];
            if (value !== undefined && value !== '') return { name: value, named: true };
        }
        return { name: this.seriesKey(panel, labels) || panel.title, named: false };
    }

    // seriesKey identifies the row by its index labels, which are unique; names are not.
    seriesKey(panel, labels) {
        return (panel.index_labels || [])
            .map(label => labels[label])
            .filter(value => value !== undefined && value !== '')
            .join('/');
    }

    // feedTables records a panel's current row values for the tables it belongs to.
    feedTables(target, metrics) {
        const panel = target.panel;
        if (!panel || !panel.index_labels || panel.index_labels.length === 0) return;

        for (const [paneKey, pane] of this.paneTables) {
            if (!pane.tables.some(table =>
                (table.columns || []).some(column => column.metric === panel.metric))) {
                continue;
            }
            const [deviceId, ...rest] = paneKey.split('-');
            const mibName = rest.join('-');

            for (const table of pane.tables) {
                if (!(table.columns || []).some(column => column.metric === panel.metric)) continue;
                const key = this.tableKey(deviceId, mibName, table);
                for (const metric of metrics) {
                    const labels = metric.metric || {};
                    const rowKey = this.seriesKey(panel, labels);
                    if (!rowKey) continue;
                    const text = panel.value_label
                        ? (labels[panel.value_label] ?? '')
                        : this.formatPanelValue(panel, metric.value);
                    this.recordTableRow(key, rowKey, this.seriesName(panel, labels),
                        panel.metric, text);
                }
                // Seeded here, from the rows just recorded, so a table filters its
                // charts whether or not it has drawn anything.
                const rows = this.tableRows.get(key) || new Map();
                this.seedSelection(key, this.orderedRowKeys(rows));

                this.renderTableBody(deviceId, mibName, table, pane.panels);
            }
        }
    }

    // chartedRows is the set of rows a panel may draw, or null when the panel
    // belongs to no table and draws everything.
    chartedRows(panel) {
        for (const [paneKey, pane] of this.paneTables) {
            for (const table of pane.tables) {
                if (!(table.columns || []).some(column => column.metric === panel.metric)) continue;
                const [deviceId, ...rest] = paneKey.split('-');
                const key = this.tableKey(deviceId, rest.join('-'), table);
                return this.tableSelection.get(key) || null;
            }
        }
        return null;
    }

    // updatePanel routes one query's result to the widget that asked for it.
    updatePanel(target, metrics, time) {
        // An empty reading means no current series: clear the panel.
        if (metrics.length === 0) {
            this.clearPanel(target);
            return;
        }

        // Every panel feeds the table, whatever it renders as.
        this.feedTables(target, metrics);

        if (target.panel.type === 'state' || target.panel.type === 'text') {
            this.updatePanelTile(target, metrics);
            return;
        }
        this.updatePanelChart(target, metrics, time);
    }

    // clearPanel says a panel that had readings no longer has one.
    clearPanel(target) {
        const element = document.getElementById(target.elementId);
        if (!element) return;

        if (target.panel.type === 'state' || target.panel.type === 'text') {
            delete element.dataset.waiting;
            element.textContent = NO_DATA_TITLE;
            element.title = NO_DATA_DETAIL;
            const tile = element.closest('.status-item');
            if (tile) tile.classList.add('is-nodata');
            return;
        }

        // Keep the plotted history and mark it last-known; with no history, go terminal.
        const chart = this.charts[target.elementId];
        const plotted = chart && chart._panelSeries && chart._panelSeries.size > 0;
        if (plotted) {
            this.markChartStale(target.elementId, this.newestPlotted(chart));
            return;
        }
        if (chart && chart._panelSeries) {
            chart._panelSeries.clear();
            chart._hasData = false;
            chart.clear();
        }
        this.markChartNoData(target.elementId);
    }

    // newestPlotted is the timestamp of the most recent point on any series,
    // which is what "last known" means for the card as a whole.
    newestPlotted(chart) {
        let newest = 0;
        for (const row of chart._panelSeries.values()) {
            const last = row.points[row.points.length - 1];
            if (last && last[0] > newest) newest = last[0];
        }
        return newest;
    }

    // markChartStale labels the plot last-known with its time.
    markChartStale(chartId, newest) {
        const card = this.chartCard(chartId);
        if (!card) return;

        card.classList.add('is-stale');
        const banner = card.querySelector('.chart-stale');
        const text = card.querySelector('.chart-stale-text');
        if (text) {
            text.textContent = newest
                ? `Last known ${new Date(newest).toLocaleTimeString()} — no current reading`
                : 'No current reading';
        }
        if (banner) banner.hidden = false;
    }

    // clearChartStale takes the label off when readings resume.
    clearChartStale(chartId) {
        const card = this.chartCard(chartId);
        if (!card) return;

        card.classList.remove('is-stale');
        const banner = card.querySelector('.chart-stale');
        if (banner) banner.hidden = true;
    }

    // updatePanelTile lists a state or text panel's rows.
    updatePanelTile(target, metrics) {
        const element = document.getElementById(target.elementId);
        if (!element) return;

        const panel = target.panel;
        const rows = this.distinctNames(panel, (metrics || []).flatMap(metric => {
            const labels = metric.metric || {};
            const identity = this.seriesIdentity(panel, labels);
            const key = this.seriesKey(panel, labels) || panel.title;

            if (panel.type !== 'text') {
                return [{
                    key,
                    name: identity.name,
                    reading: this.decodeState(panel, metric.value)
                }];
            }

            // A text reading with no name of its own IS the identity. Putting
            // the row's index in front of it -- "369098752  port-channel1" --
            // adds a number nobody can use to the only thing worth reading.
            const anonymous = !identity.named;

            // A series with several readings yields one row per reading, named by its label.
            const readings = this.textReadings(panel, labels);
            const labelled = readings.length > 1;

            // On an indexed panel, keep the row identity in front of the reading's label.
            const indexed = (panel.index_labels || []).length > 0 && key !== panel.title;

            return readings.map(reading => ({
                key,
                name: labelled && indexed
                    ? `${identity.name} ${reading.label}`
                    : reading.label || (anonymous ? '' : identity.name),
                reading: reading.text,
                labelled
            }));
        })).filter(row => row.reading !== undefined && row.reading !== '');

        if (rows.length === 0) {
            return;
        }

        delete element.dataset.waiting;
        this.clearStatusNoData(element);

        // One row needs no name in front of it: the card's own title says what
        // it is, and repeating it reads as a table with one useless column.
        if (rows.length === 1) {
            element.textContent = rows[0].reading;
            return;
        }

        // Show whole rows only and say how many are hidden.
        const expanded = element.dataset.expanded === '1';
        const shown = expanded ? rows : rows.slice(0, PANEL_ROWS_VISIBLE);
        const hidden = rows.length - shown.length;

        element.innerHTML =
            '<span class="panel-rows">' + shown.map(row =>
                `<span class="panel-row${row.name ? '' : ' is-unnamed'}${row.labelled ? ' is-reading' : ''}">` +
                (row.name ? `<span class="panel-row-name">${this.escapeHtml(row.name)}</span>` : '') +
                (row.labelled
                    ? this.readingValueMarkup(row, panel)
                    : `<span class="panel-row-value">${this.escapeHtml(row.reading)}</span>`) +
                '</span>'
            ).join('') + '</span>' +
            (hidden > 0
                ? `<button type="button" class="panel-rows-more" data-panel-more="1">${hidden} more</button>`
                : (expanded && rows.length > PANEL_ROWS_VISIBLE
                    ? '<button type="button" class="panel-rows-more" data-panel-more="1">Show fewer</button>'
                    : ''));

        this.markClippedReadings(element);
    }

    // readingValueMarkup renders a clipped reading as a control that opens it in the sheet under its own name.
    readingValueMarkup(row, panel) {
        const meta = [['Metric', panel.metric], ['OID', panel.oid]]
            .filter(([, value]) => value !== undefined && value !== null && value !== '');

        return `<button type="button" class="desc-trigger panel-row-value"
                    aria-haspopup="dialog"
                    aria-label="Open the full ${this.escapeAttr(row.name)} reading"
                    data-desc-title="${this.escapeAttr(row.name)}"
                    data-desc-eyebrow="Reading"
                    data-desc-meta="${this.escapeAttr(JSON.stringify(meta))}"
                    data-desc-text="${this.escapeAttr(row.reading)}"
                >${this.escapeHtml(row.reading)}</button>`;
    }

    // markClippedReadings measures which readings the card actually cut and flags them.
    markClippedReadings(element) {
        for (const value of element.querySelectorAll('.panel-row.is-reading .panel-row-value')) {
            value.classList.toggle('is-clipped', value.scrollHeight > value.clientHeight + 1);
        }
    }

    // checkPanelTileTimeout gives up on a tile that never received a sample.
    checkPanelTileTimeout(elementId) {
        const element = document.getElementById(elementId);
        if (!element || !element.dataset.waiting) return;

        this.markStatusNoData(elementId);
    }

    // textReadings returns an info metric's readings: the value label, or each non-bookkeeping label separately, as published.
    textReadings(panel, labels) {
        const named = panel.value_label && labels[panel.value_label];
        if (named) return [{ label: '', text: named }];

        const skip = new Set([
            '__name__', 'device', 'oid', 'source', 'instance', 'job',
            ...(panel.index_labels || [])
        ]);
        // Every reading keeps its label, since device tags also arrive as labels.
        return Object.keys(labels).sort()
            .filter(name => !skip.has(name) && labels[name] !== '')
            .map(name => ({ label: name, text: labels[name] }));
    }

    // distinctNames adds the index to display names that collide.
    distinctNames(panel, rows) {
        const counts = new Map();
        for (const row of rows) {
            if (row.name) counts.set(row.name, (counts.get(row.name) || 0) + 1);
        }
        return rows.map(row => row.name && counts.get(row.name) > 1 && row.name !== row.key
            ? { ...row, name: `${row.name} (${row.key})` }
            : row);
    }

    // decodeState turns a state number into its catalog label, or shows the number.
    decodeState(panel, value) {
        const decode = panel.enum || {};
        const key = String(Math.floor(Number(value)));
        return decode[key] !== undefined ? decode[key] : String(value);
    }

    // updatePanelChart plots each row of a panel as its own line.
    updatePanelChart(target, metrics, time) {
        const chart = this.chartFor(target.elementId);
        if (!chart || !metrics || metrics.length === 0) return;

        const panel = target.panel;
        if (!chart._panelSeries) chart._panelSeries = new Map();

        for (const metric of metrics) {
            const labels = metric.metric || {};
            const key = this.seriesKey(panel, labels) || panel.title;
            let row = chart._panelSeries.get(key);
            if (!row) {
                row = { name: this.seriesName(panel, labels), points: [] };
                chart._panelSeries.set(key, row);
            } else {
                // The name can arrive later than the reading: the exporter
                // promotes a status column onto the rows it polled it for, so
                // a row can be nameless on one tick and named on the next.
                row.name = this.seriesName(panel, labels);
            }
            row.points.push([time, metric.value]);
            // The same window every series has always used. A panel of forty
            // rows would otherwise hold forty times the history for the same
            // span of time on screen.
            if (row.points.length > PANEL_POINT_CAP) row.points.shift();
        }

        if (!chart._hasData) {
            this.revealChart(target.elementId);
            chart._hasData = true;
            chart._timeBand = netmonCharts.timeBand(0);
            chart._spanMs = 0;
        }
        this.clearChartStale(target.elementId);

        // Throttled for the same reason single-series charts are: a tick is
        // five seconds and a redraw of forty lines is not free.
        const now = Date.now();
        if (chart._lastUpdate && now - chart._lastUpdate < 2000) return;
        chart._lastUpdate = now;

        chart.setOption(this.buildPanelOption(chart, panel), true);
    }

    // backfillPanel loads a chart's history from the range endpoint so it does not start empty.
    async backfillPanel(target) {
        const panel = target.panel;
        if (!panel || !panel.query) return;

        const response = await fetch('/api/metrics/query', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ metric: panel.query, time_range: BACKFILL_RANGE })
        });
        if (!response.ok) {
            throw new Error(`${response.status} ${await response.text()}`);
        }

        const series = await response.json();
        if (!Array.isArray(series) || series.length === 0) return;

        const chart = this.chartFor(target.elementId);
        if (!chart) {
            // Hold the backfill until the card's chart exists; chartFor applies it.
            this.pendingBackfill.set(target.elementId, { panel, series });
            return;
        }
        this.applyBackfill(chart, target.elementId, panel, series);
    }

    // applyBackfill merges a range read into a chart's series, sorted and de-duplicated by timestamp.
    applyBackfill(chart, elementId, panel, series) {
        if (!chart._panelSeries) chart._panelSeries = new Map();

        for (const line of series) {
            const labels = line.labels || {};
            const key = this.seriesKey(panel, labels) || panel.title;
            const history = (line.values || [])
                .map(point => [Number(point[0]) * 1000, parseFloat(point[1])])
                .filter(point => Number.isFinite(point[0]) && Number.isFinite(point[1]));
            if (history.length === 0) continue;

            let row = chart._panelSeries.get(key);
            if (!row) {
                row = { name: this.seriesName(panel, labels), points: [] };
                chart._panelSeries.set(key, row);
            }

            // Whatever is already plotted keeps its timestamp: it came from the
            // live stream, which is the same reading by a shorter path.
            const plotted = new Set(row.points.map(point => point[0]));
            row.points = history
                .filter(point => !plotted.has(point[0]))
                .concat(row.points)
                .sort((a, b) => a[0] - b[0]);
            if (row.points.length > PANEL_POINT_CAP) {
                row.points = row.points.slice(-PANEL_POINT_CAP);
            }
        }

        if (!chart._hasData) {
            this.revealChart(elementId);
            chart._hasData = true;
            chart._timeBand = netmonCharts.timeBand(0);
            chart._spanMs = 0;
        }
        chart._lastUpdate = 0;
        chart.setOption(this.buildPanelOption(chart, panel), true);
    }

    // legendOption picks a panel's legend: none for one line, plain names for a few, the pager beyond.
    legendOption(count) {
        if (count <= 1) return { show: false };
        if (count <= MAX_SELECTED_ROWS) {
            return { top: 0, itemHeight: 8, itemWidth: 14 };
        }
        return { type: 'scroll', top: 0, itemHeight: 8, itemWidth: 14 };
    }

    // buildPanelOption is the ECharts option for a multi-series panel.
    buildPanelOption(chart, panel) {
        // Draw only the ticked rows of a table-bound panel, including none; unbound panels draw everything.
        const charted = this.chartedRows(panel);
        const keys = [...chart._panelSeries.keys()]
            .filter(key => !charted || charted.has(key))
            .sort();

        // The same rule the tiles use: a name two rows share is qualified with
        // the index that tells them apart.
        const labelled = new Map();
        for (const row of this.distinctNames(null,
                keys.map(key => ({ key, name: chart._panelSeries.get(key).name })))) {
            labelled.set(row.key, row.name);
        }

        // Render an empty plot when nothing is ticked.
        if (keys.length === 0) {
            return {
                series: [],
                legend: { show: false },
                xAxis: { type: 'time', show: false },
                yAxis: { type: 'value', show: false },
                graphic: {
                    type: 'text', left: 'center', top: 'middle',
                    style: {
                        text: 'No rows charted',
                        fill: netmonCharts.axisColor ? netmonCharts.axisColor() : '#888',
                        fontSize: 12,
                    },
                },
            };
        }

        const longest = keys.reduce(
            (span, key) => Math.max(span, this.seriesSpan(chart._panelSeries.get(key).points)), 0);

        return {
            tooltip: {
                trigger: 'axis',
                axisPointer: { type: 'line' },
                formatter: (params) => {
                    if (!params || params.length === 0) return '';
                    const when = netmonCharts.timestampLabel(params[0].value[0], chart._spanMs || 0);
                    const lines = params.map(p =>
                        `${this.escapeHtml(p.seriesName)}: <b>${this.formatPanelValue(panel, p.value[1])}</b>`);
                    return `${when}<br/>${lines.join('<br/>')}`;
                }
            },
            // The scrolling legend only for panels not bound to a table.
            legend: this.legendOption(keys.length),
            grid: { left: 4, right: 14, top: keys.length > 1 ? 30 : 18, bottom: 2, containLabel: true },
            xAxis: this.timeAxisOption(longest, chart._panelSeries.get(keys[0]).points),
            yAxis: {
                type: 'value',
                // The rate unit, not the raw one.
                name: this.panelUnit(panel),
                nameLocation: 'end',
                nameGap: 8,
                axisLabel: { formatter: value => this.formatAxisValue(value, panel.unit) }
            },
            series: keys.map((key, index) => ({
                name: labelled.get(key),
                type: 'line',
                smooth: true,
                symbol: 'circle',
                symbolSize: 4,
                showSymbol: chart._panelSeries.get(key).points.length < 2,
                data: chart._panelSeries.get(key).points,
                lineStyle: { width: 1.5 },
                // An area fill under forty overlapping lines is a smear, so it
                // is kept for the single-series case where it reads well.
                areaStyle: keys.length === 1 ? netmonCharts.areaStyle(chart, 0) : undefined,
                itemStyle: { color: netmonCharts.seriesColor(index) }
            }))
        };
    }

    // formatPanelValue renders a reading with the catalog's declared unit.
    formatPanelValue(panel, value) {
        const number = parseFloat(value);
        if (isNaN(number)) return String(value);

        const rendered = Math.abs(number) >= 1000 || Number.isInteger(number)
            ? number.toLocaleString(undefined, { maximumFractionDigits: 2 })
            : number.toFixed(3);

        if (!panel.unit) return panel.type === 'rate' ? `${rendered}/s` : rendered;
        return panel.type === 'rate' ? `${rendered} ${panel.unit}/s` : `${rendered} ${panel.unit}`;
    }

    checkAndHideEmptyMibTab(deviceId, mibName) {
        const tabPane = document.getElementById(`mib-tab-${mibName}`);
        if (!tabPane) return;

        // Check if tab has any visible content
        const visibleSections = tabPane.querySelectorAll(
            '.status-grid:not(.no-data-hidden):not([style*="display: none"]), ' +
            '.charts-grid:not(.no-data-hidden):not([style*="display: none"])'
        );

        // Also check if there are visible items within each section
        let hasVisibleContent = false;
        for (const section of visibleSections) {
            const visibleItems = section.querySelectorAll(
                '.panel:not(.no-data-hidden):not([style*="display: none"])'
            );
            if (visibleItems.length > 0) {
                hasVisibleContent = true;
                break;
            }
        }

        if (!hasVisibleContent) {
            console.log(`MIB tab ${mibName} has no visible content, hiding tab`);
            // Hide the tab button
            const tabButtons = document.querySelectorAll('#mibTabList .tab-button');
            tabButtons.forEach(btn => {
                if ((btn.dataset.mib || btn.textContent) === mibName) {
                    btn.style.display = 'none';
                    btn.classList.add('no-data-hidden');
                }
            });

            // Hide the tab pane
            tabPane.style.display = 'none';
            tabPane.classList.add('no-data-hidden');

            // Switch to next available tab if this was the active one
            if (this.activeMibTab === mibName) {
                const visibleTabButton = document.querySelector('#mibTabList .tab-button:not(.no-data-hidden):not([style*="display: none"])');
                if (visibleTabButton) {
                    visibleTabButton.click();
                }
            }

            // The rail's own count has to agree with what is left in it.
            const search = document.getElementById('mibTabSearch');
            this.filterMibTabs(search ? search.value : '');
        }
    }

    unsubscribeFromDashboard(deviceId, mibName) {
        const key = `${deviceId}-${mibName}`;
        const subscriptionData = this.activeSubscriptions.get(key);

        if (!subscriptionData) {
            console.log(`No active subscription found for ${key}`);
            return;
        }

        const { metricQueries, statusQueries } = subscriptionData;
        const allQueries = [...metricQueries, ...statusQueries];

        // The cards these fed are gone, so the readings held for them are dead
        // weight -- and a reader moving between devices all day would otherwise
        // accumulate one per query per device visited.
        for (const query of allQueries) {
            this.lastReadings.delete(query);
            this.pendingResync.delete(query);
        }

        console.log(`Unsubscribing from dashboard: ${key} with ${allQueries.length} queries`);

        if (this.ws && this.ws.readyState === WebSocket.OPEN) {
            this.ws.send(JSON.stringify({
                type: 'unsubscribe',
                queries: allQueries
            }));
        }

        this.activeSubscriptions.delete(key);
        console.log(`Active subscriptions now:`, Array.from(this.activeSubscriptions.keys()));
    }

    unsubscribeFromAllDashboards(deviceId) {
        console.log(`Unsubscribing from all dashboards for device: ${deviceId}`);

        const keysToRemove = [];
        this.activeSubscriptions.forEach((subscriptionData, key) => {
            if (key.startsWith(`${deviceId}-`)) {
                keysToRemove.push(key);
            }
        });

        keysToRemove.forEach(key => {
            // The key is `${deviceId}-${mibName}`; the name is everything past the first hyphen.
            const mibName = key.slice(String(deviceId).length + 1);
            this.unsubscribeFromDashboard(deviceId, mibName);
        });
    }

    // initializeEmptyChart registers a card and builds its ECharts instance on first view or first sample.
    initializeEmptyChart(chartId, title) {
        const chartElement = document.getElementById(chartId);
        if (!chartElement) {
            console.warn(`Chart element not found: ${chartId}`);
            return;
        }

        // The card starts collapsed; resetChartCard is what puts it in its
        // waiting state, and that is DOM rather than a plot.
        this.resetChartCard(chartId);

        this.pendingCharts.set(chartId, title);
        this.chartViewport().observe(chartElement);

        // The no-data deadline is about the card, not the plot, so it runs
        // whether or not anything has been built yet.
        setTimeout(() => {
            this.checkChartDataTimeout(chartId);
        }, FIRST_SAMPLE_TIMEOUT);
    }

    // discardChart drops a card's plot and any pending registration for it, so
    // a rebuild starts from nothing. Disposing the instance alone would leave a
    // registration behind that rebuilds the card the caller just replaced.
    discardChart(chartId) {
        const existing = this.charts[chartId];
        if (existing) {
            existing.dispose();
            delete this.charts[chartId];
        }
        this.pendingCharts.delete(chartId);

        const element = document.getElementById(chartId);
        if (element && this._chartViewport) {
            this._chartViewport.unobserve(element);
        }
    }

    // chartViewport is the shared observer that builds a plot when its card
    // first comes into view. One observer serves every card on the page.
    chartViewport() {
        if (!this._chartViewport) {
            this._chartViewport = new IntersectionObserver(entries => {
                for (const entry of entries) {
                    if (entry.isIntersecting) {
                        this.chartFor(entry.target.id);
                    }
                }
            }, {
                // Build a little before the card is reached, so scrolling does
                // not expose an empty frame.
                root: null, rootMargin: '400px 0px', threshold: 0,
            });
        }
        return this._chartViewport;
    }

    // chartFor returns a card's plot, building it if this is the first need.
    // It returns null for a card that was never registered.
    chartFor(chartId) {
        const existing = this.charts[chartId];
        if (existing) return existing;

        const title = this.pendingCharts.get(chartId);
        if (title === undefined) return null;

        this.pendingCharts.delete(chartId);
        const chart = this.buildChart(chartId, title);

        // History that arrived before this card had an element to draw on.
        const held = this.pendingBackfill.get(chartId);
        if (chart && held) {
            this.pendingBackfill.delete(chartId);
            this.applyBackfill(chart, chartId, held.panel, held.series);
        }
        return chart;
    }

    // buildChart creates the ECharts instance for a registered card.
    buildChart(chartId, title) {
        const chartElement = document.getElementById(chartId);
        if (!chartElement) {
            console.warn(`Chart element not found: ${chartId}`);
            return null;
        }

        this.chartViewport().unobserve(chartElement);

        const containerWidth = chartElement.offsetWidth || chartElement.parentElement?.offsetWidth || 800;
        const initOpts = { width: containerWidth, height: 300 };
        const chart = echarts.init(chartElement, 'dark', initOpts);

        // A placeholder is not a chart: the waiting and no-data states are DOM,
        // so an empty card costs two lines of text instead of an empty plot.
        chart.setOption({
            xAxis: { type: 'time', show: false },
            yAxis: { type: 'value', show: false },
            series: [{ type: 'line', data: [], smooth: true }]
        });

        // Store chart reference with initialization timestamp
        console.log(`Storing chart with ID: ${chartId}`);
        chart._initTimestamp = Date.now();
        chart._hasData = false;
        this.charts[chartId] = chart;
        return chart;
    }

    checkChartDataTimeout(chartId) {
        const chart = this.charts[chartId];
        // A card with no plot has certainly received nothing: a sample would
        // have built one. Without this it kept saying "Waiting for data…"
        // forever, since the deadline only ever examined built plots.
        if (!chart) {
            if (this.pendingCharts.has(chartId)) {
                this.markChartNoData(chartId);
            }
            return;
        }
        if (chart._hasData) return;

        // The card keeps its place in the grid: a MIB whose objects the device
        // does not populate should read as "nothing here", not disappear.
        // The instance stays live so a late first sample still draws.
        console.log(`Chart ${chartId} timed out waiting for data`);
        this.markChartNoData(chartId);
    }

    resubscribeActiveDashboards() {
        console.log('Re-subscribing to active dashboards after WebSocket reconnection');

        if (!this.ws || this.ws.readyState !== WebSocket.OPEN) {
            console.warn('WebSocket not ready for re-subscription');
            return;
        }

        // A sequence belongs to a connection: drop held sequences on reconnect.
        this.lastReadings.clear();
        this.pendingResync.clear();

        // Re-subscribe to all active dashboards
        this.activeSubscriptions.forEach((subscriptionData, key) => {
            const { metricQueries, statusQueries } = subscriptionData;
            const allQueries = [...metricQueries, ...statusQueries];
            console.log(`Re-subscribing to ${key} with ${allQueries.length} queries`);
            this.ws.send(JSON.stringify({
                type: 'subscribe',
                queries: allQueries,
                supports: WS_SUPPORTS
            }));
        });

        console.log(`Re-subscribed to ${this.activeSubscriptions.size} dashboards`);
    }

    // ========================================
    // State Management
    // ========================================

    loadState() {
        try {
            const savedState = localStorage.getItem('go-netmon-state');
            if (savedState) {
                const state = JSON.parse(savedState);
                this.currentView = state.currentView || 'dashboard';
                this.currentTimeRange = state.currentTimeRange || '6h';
                // Clamped on the way in as well as on the way out: this value
                // came from storage a previous version wrote, or a person edited.
                this.refreshIntervalSec = this.clampRefreshSeconds(
                    state.refreshIntervalSec ?? DEFAULT_REFRESH_SECONDS);
                // Store selected device ID for restoration after devices load
                this.restoredDeviceId = state.selectedDeviceId;
                this.restoredMibTab = state.activeMibTab;
            }
        } catch (error) {
            console.error('Error loading state:', error);
        }
    }

    saveState() {
        try {
            const deviceSelect = document.getElementById('dashboardDeviceSelect');
            const state = {
                currentView: this.currentView,
                currentTimeRange: this.currentTimeRange,
                refreshIntervalSec: this.refreshIntervalSec,
                selectedDeviceId: deviceSelect ? deviceSelect.value : null,
                activeMibTab: this.activeMibTab,
            };
            localStorage.setItem('go-netmon-state', JSON.stringify(state));
        } catch (error) {
            console.error('Error saving state:', error);
        }
    }

    restoreState() {
        // Restore view
        if (this.currentView !== 'dashboard') {
            this.switchView(this.currentView);
        }

        // Write restored values back to their controls.
        const timeRange = document.getElementById('dashboardTimeRange');
        if (timeRange) timeRange.value = this.currentTimeRange;

        const refresh = document.getElementById('updateInterval');
        if (refresh) refresh.value = this.refreshIntervalSec;

        // Restore device and tab selection after devices are loaded
        if (this.restoredDeviceId) {
            const deviceSelect = document.getElementById('dashboardDeviceSelect');
            if (deviceSelect && deviceSelect.querySelector(`option[value="${this.restoredDeviceId}"]`)) {
                deviceSelect.value = this.restoredDeviceId;
                this.currentDashboardDevice = this.restoredDeviceId;
                this.loadDeviceDashboards(this.restoredDeviceId);
            }
        }
    }

    // ========================================
    // UI Helpers
    // ========================================

    // showNotification shows a transient message for holdMs.
    showNotification(message, type = 'success', holdMs = 3000) {
        const container = document.getElementById('notificationContainer');
        const notification = document.createElement('div');
        notification.className = `notification ${type}`;
        notification.textContent = message;

        container.appendChild(notification);

        setTimeout(() => {
            notification.style.opacity = '0';
            setTimeout(() => notification.remove(), 300);
        }, holdMs);
    }

    async confirm(message) {
        return window.confirm(message);
    }

    escapeHtml(text) {
        if (text === null || text === undefined) return '';
        const div = document.createElement('div');
        div.textContent = text;
        return div.innerHTML;
    }

    // escapeHtml leaves quotes alone, which is safe in a text node and unsafe
    // in an attribute. MIB DESCRIPTIONs quote freely, so attributes use this.
    escapeAttr(text) {
        if (text === null || text === undefined) return '';
        return String(text)
            .replace(/&/g, '&amp;')
            .replace(/</g, '&lt;')
            .replace(/>/g, '&gt;')
            .replace(/"/g, '&quot;')
            .replace(/'/g, '&#39;');
    }
}

// Initialize the app when DOM is ready
let app;
document.addEventListener('DOMContentLoaded', () => {
    app = new MonitoringApp();
    // Make app globally accessible for inline onclick handlers
    window.app = app;
});
