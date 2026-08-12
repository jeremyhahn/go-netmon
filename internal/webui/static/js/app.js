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

// Every other list surface — the MIB library, the device and profile lists, the
// assignment pickers — pages at this size and gains a filter box once it is
// long enough to get away from the reader. Below the threshold a list fits on
// screen and the controls would be chrome rather than help.
const LIST_PAGE = 50;
const LIST_CONTROLS_MIN = 8;

// The MIB browser pages smaller than the other lists. Each of its rows is an
// accordion that expands into a table of objects, so a screenful of them is far
// more than a screenful of ordinary rows.
const MIB_ACCORDION_PAGE = 25;

// Pagination sizing, shared by every paged surface.
//
// PAGER_WINDOW numbered buttons sit around the current page, with the first and
// last page always reachable and a gap marker standing in for the run between.
// That caps the strip at nine controls whether the list is two pages long or
// ten thousand, so the pager can never become the overflow problem it exists to
// solve. Past PAGER_JUMP_MIN pages the neighbours are no longer enough to reach
// an arbitrary page, so a direct "go to page" field is offered as well.
const PAGER_WINDOW = 5;
const PAGER_JUMP_MIN = 8;

// The rows-per-page choices a surface offers, as multiples of its own natural
// page. A 50-row list offers 50/100/250; the 200-object surfaces offer
// 200/400/1000. The choice is remembered for the session, not forever: it is a
// response to the task in hand rather than a preference.
const PAGER_SIZE_STEPS = [1, 2, 5];
const PAGER_SIZE_KEY = 'netmon-page-size';

// How long a widget waits for its first sample before it stops saying it is
// still loading. Charts and status tiles share the window so the same object
// reaches its terminal state at the same moment in both shapes.
const FIRST_SAMPLE_TIMEOUT = 30000;

// The terminal wording, in one place, because a card and a tile describing the
// same silent object must not describe it differently. It says what is known —
// nothing arrived — and not why, which the UI cannot tell.
const NO_DATA_TITLE = 'No data reported';
const NO_DATA_DETAIL = 'The device returned no samples for this object.';

// The states /api/devices/status reports. The server decides which one a device
// is in; this table only says how each is worded and coloured. Three of them
// are distinctions the UI could not previously make and must not blur:
// never_polled is a device the poller holds but has not finished a cycle for,
// which is not the same as failing; not_registered is a configuration fault
// rather than a device fault; unknown means the state could not be determined
// at all and is neither healthy nor down.
const FLEET_STATES = {
    ok:             { label: 'Healthy',        tone: 'ok',      blurb: 'The last poll succeeded.' },
    failing:        { label: 'Failing',        tone: 'failing', blurb: 'A poll ran and did not succeed.' },
    unreachable:    { label: 'Unreachable',    tone: 'down',    blurb: 'Repeated polls went unanswered.' },
    never_polled:   { label: 'Never polled',   tone: 'pending', blurb: 'The poller has this device but has not completed a cycle for it yet.' },
    not_registered: { label: 'Not registered', tone: 'config',  blurb: 'The poller has no record of this device. That is a configuration fault, not a device fault.' },
    unknown:        { label: 'Unknown',        tone: 'unknown', blurb: 'The state could not be determined.' },
};

// The order the chips are laid out in. It is presentation only: the device list
// itself keeps the order the server sent, which is already deterministic.
const FLEET_STATE_ORDER = ['ok', 'failing', 'unreachable', 'never_polled', 'not_registered', 'unknown'];

// Severity, worst first. This is what the State column sorts by: triage order,
// not the alphabet. It is deliberately separate from FLEET_STATE_ORDER above,
// which lays out the chips and reads best healthy-first.
const FLEET_SEVERITY = ['unreachable', 'failing', 'not_registered', 'never_polled', 'unknown', 'ok'];

// last_poll_time and next_poll_time are Go time.Time values whose omitempty
// never fires, so a device that has never been polled sends the zero time
// rather than nothing at all. Anything this old is that zero value, not a poll
// that happened in the first century.
const ZERO_TIME_CEILING = Date.UTC(1971, 0, 1);

// How often the dashboard re-reads whichever surface is on screen. One cadence,
// shared: the fleet overview and a selected device's dashboards take turns on
// this tick rather than each running a timer of their own.
const DASHBOARD_REFRESH_MS = 30000;

class MonitoringApp {
    constructor() {
        this.ws = null;
        this.charts = {};
        this.reconnectInterval = 3000;
        this.currentView = 'dashboard';
        this.currentDevice = null;
        this.currentProfile = null;
        this.devices = [];
        this.profiles = [];
        this.mibs = [];
        this.deviceDashboards = {}; // { deviceId: { mibs: [], dashboards: {} } }

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

        // Fleet overview: the last /api/devices/status payload, in the order the
        // server sent it, plus which state chip is filtering the list and when
        // the payload arrived. fetchedAt is what "updated 12s ago" is measured
        // against, so a stale surface says so instead of looking live.
        this.fleet = { devices: [], stateFilter: '', fetchedAt: null, error: null };
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
        this.instanceSelections = {}; // { chartId: selectedInstance }

        // Track active WebSocket subscriptions per dashboard
        // Key: "deviceId-mibName", Value: { metricQueries: Set(), statusQueries: Set(), objects: Map(query -> obj) }
        this.activeSubscriptions = new Map();

        // Track current selected device for re-subscription on reconnect
        this.currentDashboardDevice = null;

        // Widgets whose spec arrived without a metric name, so the failure is
        // logged once per widget instead of once per render. See specMetric().
        this.reportedMissingMetrics = new Set();

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
        this.setupModals();

        // The landing surface is the fleet, not an empty frame waiting for a
        // choice. If a device was selected last session, restoreState() takes
        // over once the device list has loaded and hides this again.
        this.showFleetOverview();

        this.startDashboardRefresh();
    }

    // The dashboard's single refresh tick. It drives whichever surface is on
    // screen and only that one: the fleet overview while no device is selected,
    // the selected device's dashboards otherwise. Both surfaces share this
    // cadence so neither gets a timer of its own competing with the other.
    startDashboardRefresh() {
        setInterval(async () => {
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

                // Has a MIB assigned to *this* device started producing data
                // since the dashboard was drawn?
                //
                // This used to ask which modules hold data for anything on the
                // fleet and compare that with the modules drawn for one device.
                // Those sets differ by construction — 13 against 2 on a four
                // device stack — so it found "new" data on every tick and tore
                // the whole dashboard down and rebuilt it every 30 seconds,
                // discarding the streamed history along with it.
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
        }, DASHBOARD_REFRESH_MS);
    }

    // deviceNameFor resolves a device id to the name its metrics are labelled
    // with. The dashboard selector carries ids; Prometheus carries names, so
    // anything asking Prometheus about the selected device goes through here.
    //
    // The id arrives as a string from a select element and as a number from the
    // device list, so both are accepted rather than relying on the caller.
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

    appendChartData(chartName, seriesIndex, dataPoint) {
        const chart = this.charts[chartName];
        if (!chart) return;

        const option = chart.getOption();
        const data = option.series[seriesIndex].data;

        data.push(dataPoint);

        // Keep last 50 data points
        if (data.length > 50) {
            data.shift();
        }

        chart.setOption(option);
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
            this.subscribeToMetrics();

            // Re-subscribe to active dashboards after reconnection
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

    subscribeToMetrics() {
        // Warm-up subscriptions: the handful of objects the overview leans on,
        // named after the MIB object the way the exporter names every series.
        // Their row index, where they have one, is in labels, so one name here
        // covers every row of the object.
        const subscriptions = [
            'snmp_sysUpTime',
            'snmp_cpmCPUTotal5sec',
            'snmp_ciscoMemoryPoolUsed',
            'snmp_ciscoMemoryPoolFree',
        ];

        this.ws.send(JSON.stringify({
            type: 'subscribe',
            queries: subscriptions
        }));
    }

    handleMessage(message) {
        console.log('WebSocket message received:', message.type, message.query, message.metrics?.length || 0, 'metrics');
        if (message.type === 'metric_update') {
            // Handle both old MIB browser charts and new dashboard charts
            this.updateChart(message.query, message.metrics, message.timestamp);
            this.updateDashboardCharts(message.query, message.metrics, message.timestamp);
        }
    }

    updateChart(query, metrics, timestamp) {
        // Skip if metrics is not an array or is empty
        if (!metrics || !Array.isArray(metrics) || metrics.length === 0) {
            return;
        }

        const time = new Date(timestamp * 1000);

        if (query.includes('1_3_6_1_2_1_1_3_0')) {
            metrics.forEach(m => {
                const days = (m.value / 100 / 86400).toFixed(2);
                this.appendChartData('uptime', 0, [time, days]);
            });
        } else if (query.includes('1_3_6_1_4_1_9_9_109')) {
            metrics.forEach(m => {
                this.appendChartData('cpu', 0, [time, m.value]);
            });
        } else if (query.includes('1_3_6_1_4_1_9_9_48_1_1_1_5')) {
            metrics.forEach(m => {
                const mb = (m.value / 1024 / 1024).toFixed(2);
                this.appendChartData('memory', 0, [time, mb]);
            });
        } else if (query.includes('1_3_6_1_4_1_9_9_48_1_1_1_6')) {
            metrics.forEach(m => {
                const mb = (m.value / 1024 / 1024).toFixed(2);
                this.appendChartData('memory', 1, [time, mb]);
            });
        }
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

    // The app has one pagination engine and these are its parts. Both paged
    // surfaces — the list helper below and the MIB object picker — go through
    // them, so there is a single answer to what a page is, where in the set the
    // reader is, and what the controls look and read like.
    //
    // A pager is previous, a strip of numbered pages, next, and — once the
    // strip can no longer reach every page on its own — a field that takes a
    // page number directly. It replaced a "Show N more" button, which grew the
    // list rather than paging it: there was no way back, no way to jump, and
    // nothing that said where in the set you were.

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

    // pagerPages lists what the numbered strip shows: every page while they all
    // fit, and otherwise the first, the last, PAGER_WINDOW around the current
    // one, and a null wherever a run was left out. A gap of exactly one page is
    // filled with that page instead, because an ellipsis standing in for a
    // single number is wider than the number.
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

    // pagerNavHtml paints the controls. Every one of them is a real button so
    // it is reachable and operable from the keyboard without help, the page the
    // reader is on is marked aria-current, and the steps at the ends are
    // disabled rather than removed so the strip does not shift under the cursor
    // at the first and last page.
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

        // Past PAGER_JUMP_MIN pages the neighbours no longer reach an arbitrary
        // page, so the reader can name one. The field also reads back the page
        // they are on, which is why it is filled in rather than left blank.
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

    // paintPager writes a surface's pager into the nav and status elements its
    // shell created. Both elements outlive every repaint: a live region has to
    // be in the document before its text changes for a screen reader to
    // announce it, so the status is updated in place rather than replaced.
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

    // The repaint replaces the control that was just operated, so focus is put
    // back on its replacement. When a step runs out at the end of the set its
    // replacement is disabled, and focus goes to the page the reader is now on
    // rather than being dropped at the top of the document.
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

    // The size control lives beside the search box rather than in the footer,
    // because it is a property of the view and not a step through it — and
    // because the footer is rebuilt on every repaint, which would cost the
    // select its focus mid-choice.
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

    // Column sorting for the paged surfaces. A table's headers are the obvious
    // place to ask "which device has not been polled longest", and answering it
    // by reading down an unordered column does not scale past a screenful.
    //
    // Sorting happens after the filter and the search and before the page is
    // cut, so the reader sorts the set they can see rather than the page they
    // happen to be on — sorting a single page would reorder ten rows and leave
    // the largest value stranded on page four.

    // sortCompare orders two values of the same column.
    //
    // Missing values always sink, in both directions. A device that has never
    // been polled has no last-poll time, and the question behind the sort is
    // "which polled longest ago" — an absent value is not the answer to that in
    // either direction, so it belongs at the end rather than winning ascending.
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

    // sortItems returns a sorted copy. The input is left alone because it is the
    // caller's array — the fleet's device list is replaced wholesale by every
    // refresh, and sorting in place would fight that.
    //
    // Ties are broken by the position the server sent, so a column of equal
    // values does not reshuffle on every repaint.
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

    // sortHeaderHtml renders one header cell as a button.
    //
    // It is a real button inside the th rather than a click handler on the th:
    // that is what makes it reachable by keyboard and announced as something
    // operable. aria-sort on the th is what tells a screen reader which column
    // the table is ordered by and in which direction.
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

    // sortControlsHtml is sortHeadersHtml for a surface that is not a table.
    // The MIB browser is a stack of accordions with no header row to hang a th
    // on, so the same columns are offered as a row of buttons that reads as one
    // control rather than as scattered links.
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

    // sortPagedList applies a header click. Clicking the sorted column reverses
    // it; clicking another takes that column in its natural first direction —
    // ascending for names, descending for counts and times, because "most
    // metrics" and "longest ago" are what a reader wants first from those.
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
        this.paintPagedList(mountId);
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

    // Every list in the app that can grow past a screenful gets the same two
    // controls: a filter box that narrows the rows, and a pager that steps
    // through them and says which of them are on screen. The library table, the
    // device and profile lists, the assignment pickers and the MIB browser's
    // object lists all behave — and read — identically.
    //
    // Options:
    //   items       the full array, in the order the server gave it
    //   matches     (item, lowercased term) -> boolean
    //   filter      optional (item) -> boolean, narrowing the list ahead of the
    //               search box for surfaces that have a second control of their
    //               own; it is read on every paint, so the caller can change
    //               what it closes over and repaint
    //   renderRows  (page of items) -> html
    //   wrap        (rows html) -> html, for surfaces that need a shell
    //   noun        plural noun used in the status line and the no-match line
    //   placeholder search box placeholder and accessible name
    //   emptyHtml   what to show when there is nothing at all
    //   pageSize    rows per page, defaulting to LIST_PAGE
    //   sizeKey     what the session remembers the rows-per-page choice under,
    //               defaulting to the mount id; surfaces whose mount id carries
    //               a record id pass a stable one so the choice outlives the row
    //   pagerLabel  accessible name for the pager, defaulting to the noun; two
    //               pagers on one page must not answer to the same name
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
            afterPaint: options.afterPaint || null
        };
        this.pagedLists.set(mountId, state);

        if (items.length === 0) {
            mount.innerHTML = options.emptyHtml || '<p class="empty-state">Nothing here yet</p>';
            return;
        }

        // LIST_CONTROLS_MIN keeps a search box off a list of three rows, where
        // it is chrome offering to narrow something already entirely visible.
        // A surface can override that when the box is a destination rather than
        // a convenience — see the fleet overview.
        // A surface whose search box already exists in its own toolbar passes
        // searchInput and the component binds to that instead of rendering a
        // second one. The behaviour is identical either way — the box drives
        // the same state — so the two do not drift apart.
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

        // The shell is built once and only the body, the pager and the status
        // are repainted, so typing in the search box never costs the caret its
        // place and the status stays the same live region across a page change.
        // Header clicks are bound on the mount because the body — and with it
        // every header — is rebuilt on each repaint.
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
            // An external box outlives the list it drives, so its listener is
            // registered once and remembered; re-registering on every rebuild
            // would stack handlers and repaint once per past render.
            if (options.searchInput) {
                if (this.externalSearchBound.has(options.searchInput)) {
                    state.search = searchBox.value || '';
                } else {
                    this.externalSearchBound.add(options.searchInput);
                }
            }
            searchBox.addEventListener('input', () => {
                state.search = searchBox.value;
                // A narrower set is a different set: it is read from its start.
                state.page = 0;
                this.paintPagedList(mountId);
            });
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
                this.paintPagedList(mountId);
            });
        }

        this.bindPager(mount.querySelector('.paged-list-footer'), (page) => {
            state.page = page;
            this.paintPagedList(mountId);
        });

        this.paintPagedList(mountId);
    }

    paintPagedList(mountId) {
        const state = this.pagedLists.get(mountId);
        if (!state) return;

        const body = document.getElementById(`${mountId}Body`);
        const footer = document.getElementById(`${mountId}Footer`);
        if (!body || !footer) return;

        // The filter runs first and the search narrows what it left, so the two
        // compose instead of replacing one another, and the counts below
        // describe the set the reader can actually see.
        const term = state.search.trim().toLowerCase();
        const scoped = state.filter ? state.items.filter(state.filter) : state.items;
        const matched = term ? scoped.filter(item => state.matches(item, term)) : scoped;

        const ordered = state.sortKey ? this.sortItems(matched, state) : matched;

        const slice = this.pagerSlice(ordered.length, state.page, state.size);
        state.page = slice.page;

        body.innerHTML = ordered.length === 0
            ? `<p class="empty-state">No ${this.escapeHtml(state.noun)} match “${this.escapeHtml(state.search.trim())}”</p>`
            : state.wrap(state.renderRows(ordered.slice(slice.from, slice.to)), mountId);

        // A list that fits on one page and is not being filtered has no
        // position to report and nowhere to step to, so it gets no footer.
        if (state.afterPaint) state.afterPaint();

        footer.hidden = slice.pages <= 1 && ordered.length === state.items.length;
        this.paintPager(`${mountId}Pager`, `${mountId}Status`, {
            slice,
            matched: ordered.length,
            total: state.items.length,
            noun: state.noun,
            label: state.label
        });
    }

    // ========================================
    // Description sheet
    // ========================================

    // A MIB DESCRIPTION is usually several sentences and sometimes a whole
    // paragraph, and every list that shows one has to clip it to keep its rows
    // aligned. descriptionTriggerHtml renders the clipped line as a control
    // rather than as dead text, so the full wording is one click — or one
    // Enter — away in the description sheet, where it can be read, selected and
    // copied. That is the part a title tooltip cannot do: it is unreachable on
    // a touch screen, it cannot be selected, and browsers truncate it.
    //
    // The clipping itself is left alone. On a chart card the single line is
    // load-bearing: it is what makes every plot in a row start at the same
    // height. The fix is reaching the text, not unclamping it.
    descriptionTriggerHtml(description, detail = {}) {
        const text = String(description == null ? '' : description).trim();
        if (text === '') return '';

        const meta = (detail.meta || [])
            .filter(([, value]) => value !== undefined && value !== null && value !== '');

        return `<button type="button" class="desc-trigger${detail.className ? ' ' + detail.className : ''}"
                    aria-haspopup="dialog" title="Read the full description"
                    data-desc-title="${this.escapeAttr(detail.title || '')}"
                    data-desc-eyebrow="${this.escapeAttr(detail.eyebrow || '')}"
                    data-desc-meta="${this.escapeAttr(JSON.stringify(meta))}"
                    data-desc-text="${this.escapeAttr(text)}"
                >${this.escapeHtml(text)}</button>`;
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
        // The DESCRIPTION arrives with the MIB file's own line wrapping, which
        // is meaningless once it is in a proportional column, so it is
        // reflowed. Blank lines are the author's paragraphs and are kept.
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

    // ========================================
    // Fleet overview
    // ========================================
    //
    // The dashboard's landing surface. Every configured device, with the state
    // the poller decided it is in, so opening the app answers "is my network
    // OK" before anything is selected.
    //
    // It is a table rather than a grid of cards because the question is a
    // comparative one: aligned columns let a reader run down the state column
    // and stop on the rows that differ, which a wrapped grid of cards stops
    // being able to do as soon as there are more devices than fit on a screen.
    // Above it sits one chip per state, which is a constant amount of chrome
    // whether the fleet is four devices or four hundred, and each chip filters
    // the table beneath it. Nothing is averaged into a score: the chips are
    // counts of the states the server reported, and a single down device is a
    // chip of its own rather than a rounding error in a percentage.

    // showFleetOverview makes the overview the visible surface and refreshes it.
    // Called on load, when the device selector is cleared, and when the
    // Dashboard tab is re-entered with nothing selected.
    showFleetOverview() {
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
                const response = await fetch('/api/devices/status');
                if (!response.ok) throw new Error(`the status endpoint answered ${response.status}`);
                const data = await response.json();
                this.fleet.devices = Array.isArray(data.devices) ? data.devices : [];
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

        const count = this.fleet.devices.length;
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
                + `refreshing every ${Math.round(DASHBOARD_REFRESH_MS / 1000)}s`;
    }

    // A chip per state, each one a filter over the table below. Healthy is
    // always present so "none of them are" is as visible as "all of them are";
    // the rest appear when the fleet is actually in them, so a healthy fleet is
    // not a row of zeroes.
    renderFleetSummary() {
        const summary = document.getElementById('fleetSummary');
        if (!summary) return;

        // A strip counting nothing is chrome. With no fleet on screen — because
        // none is configured, or because the read failed — the list below says
        // which of those two it is, and says it once.
        if (this.fleet.devices.length === 0) {
            summary.innerHTML = '';
            return;
        }

        const counts = new Map();
        for (const device of this.fleet.devices) {
            const key = this.fleetStateMeta(device.state).key;
            counts.set(key, (counts.get(key) || 0) + 1);
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

        const total = this.fleet.devices.length;
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
            this.paintPagedList('fleetDeviceList');
            return;
        }

        this.pagedLists.delete('fleetDeviceList');
        this.renderPagedList('fleetDeviceList', {
            items: this.fleet.devices,
            noun: 'devices',
            placeholder: 'Search devices by name, address or state…',
            filter: device => !this.fleet.stateFilter
                || this.fleetStateMeta(device.state).key === this.fleet.stateFilter,
            matches: (device, term) => this.fleetSearchText(device).includes(term),
            // The search box is always offered here, unlike the other lists.
            // This is the surface the app opens on and the one a reader arrives
            // at knowing the device they want, so the box is a destination
            // rather than chrome — and a fleet only ever grows.
            alwaysControls: true,
            sortColumns: this.fleetSortColumns(),
            sortKey: 'state',
            sortDir: 'asc',
            wrap: (rows, mountId) => `<table class="fleet-table">
                    <thead><tr>${this.sortHeadersHtml(mountId)}</tr></thead>
                    <tbody>${rows}</tbody>
                </table>`,
            renderRows: items => items.map(device => this.renderFleetRow(device)).join(''),
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

    // The fleet table's columns. Order here is the order on screen.
    //
    // State sorts by severity rather than by its label, because the reason to
    // sort that column is triage: "show me what is broken" must not be answered
    // with an alphabetical list that puts Failing above Healthy above
    // Unreachable. Times and counts open descending, since "polled longest ago"
    // and "most series" are what is wanted first from them.
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

    renderFleetRow(device) {
        const meta = this.fleetStateMeta(device.state);
        const status = device.status || {};
        const metrics = status.metrics_collected;

        return `
            <tr class="fleet-row" data-fleet-device="${this.escapeAttr(device.id)}">
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

    // Why a device is in the state it is in, without turning the default view
    // into a wall of error text. The one line that fits is a control: the whole
    // message opens in the description sheet, which already exists for exactly
    // this and can be read, selected and copied there.
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

    // Toggling a chip repaints the rows; it never re-sorts them and never
    // re-fetches. Clicking the active chip again clears the filter.
    setFleetStateFilter(stateKey) {
        this.fleet.stateFilter = this.fleet.stateFilter === stateKey ? '' : stateKey;

        // A chip narrows the set, so the list is read from its start again.
        const state = this.pagedLists.get('fleetDeviceList');
        if (state) state.page = 0;

        this.renderFleetSummary();
        this.paintPagedList('fleetDeviceList');
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
                + `<th>SNMP Version</th><th>Actions</th></tr></thead><tbody>${rows}</tbody></table>`,
            renderRows: items => items.map(device => `<tr>
                <td><strong>${this.escapeHtml(device.name)}</strong></td>
                <td>${this.escapeHtml(device.address)}</td>
                <td>${device.port || 161}</td>
                <td><span class="badge success">${device.snmp_version || 'v2c'}</span></td>
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

            form.querySelector('input[name="username"]').value = device.username || '';
            form.querySelector('select[name="auth_protocol"]').value = device.auth_protocol || 'SHA';
            form.querySelector('select[name="priv_protocol"]').value = device.priv_protocol || 'AES';
            // The password fields stay empty. The stored secret is not put back
            // into the page, and blank means "unchanged" on save.
            form.querySelector('input[name="auth_password"]').value = '';
            form.querySelector('input[name="priv_password"]').value = '';

            this.syncDeviceVersionFields();

            // readOnly, not disabled. A disabled input is omitted from
            // FormData entirely, so the name came back null and every edit was
            // refused with "name is required" — whatever field was being
            // changed. readOnly stops it being edited and keeps it submitted,
            // which is what was wanted: the name identifies the device and
            // renaming here would be renaming the thing being edited.
            form.querySelector('input[name="name"]').readOnly = true;

            this.showModal('deviceModal');
        } catch (error) {
            console.error('Error loading device for edit:', error);
            this.showNotification('Failed to load device', 'error');
        }
    }

    // Show the credentials the chosen SNMP version actually uses.
    //
    // v1 and v2c authenticate with a community string; v3 with a user, an
    // authentication passphrase and a privacy passphrase. A form offering all
    // of them at once invites filling in both and wondering which took effect.
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
        };

        // Only the credentials the chosen version uses are sent. Sending a
        // community string with a v3 device would store one that nothing reads
        // and that an export would then carry around as a secret.
        if (version === 'v3') {
            device.username = formData.get('username') || '';
            device.auth_protocol = formData.get('auth_protocol') || '';
            device.priv_protocol = formData.get('priv_protocol') || '';

            // A blank password on an edit means "keep the stored one": the form
            // is never filled with the existing secret, so blank cannot be
            // distinguished from unchanged, and clearing a password is not
            // something this form offers.
            const auth = formData.get('auth_password');
            const priv = formData.get('priv_password');
            if (auth) device.auth_password = auth;
            if (priv) device.priv_password = priv;
        } else {
            device.community = formData.get('community');
        }

        if (isEdit) {
            // A number, not the string FormData handed back. storage.Device.ID
            // is an int64, so JSON with a quoted id fails to decode and the
            // server answers "invalid request body" — which reads as though
            // something about the device were wrong rather than the id's type.
            // Every edit failed this way, whatever field was being changed.
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
                throw new Error(`HTTP error! status: ${response.status}`);
            }

            this.showNotification('Device deleted successfully', 'success');
            await this.loadStoredDevices();
        } catch (error) {
            console.error('Error deleting device:', error);
            this.showNotification('Failed to delete device', 'error');
        }
    }

    // ========================================
    // Profile Management
    // ========================================

    async loadProfiles() {
        try {
            const response = await fetch('/api/profiles');
            const data = await response.json();
            this.profiles = data.profiles || [];
            this.renderProfilesTable(this.profiles);
        } catch (error) {
            console.error('Error loading profiles:', error);
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

    async loadStoredMIBs() {
        try {
            const response = await fetch('/api/mibs');
            const data = await response.json();
            this.mibs = data.mibs || [];
            this.renderStoredMIBsTable(this.mibs);
        } catch (error) {
            console.error('Error loading stored MIBs:', error);
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

            // The assignment reports what it will produce. A module can be
            // assigned successfully and yield nothing — a registry of OBJECT
            // IDENTIFIERs, or one whose IMPORTS did not resolve — and the only
            // symptom used to be an empty dashboard minutes later.
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

    // ========================================
    // MIB Polling Controls (enable/disable + filters)
    //
    // `scope` is 'device' or 'profile' throughout this section. Both share
    // the same assignment shape and the same two endpoints
    // (/api/devices/{id}/mibs/{mibId}/assignment and the /profiles
    // equivalent), so one set of handlers drives both the device's direct
    // MIB list and a profile's MIB list.
    // ========================================

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

    // buildAssignmentPayload always produces the *complete* assignment body:
    // the PUT endpoint replaces the stored assignment wholesale rather than
    // merging, so every field the row already carries has to be re-sent
    // alongside whatever `overrides` this particular action is changing.
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

    // toggleDirectMib handles the enabled/disabled switch on a direct MIB
    // row. Enabling a MIB on a device fetches a series estimate and requires
    // confirmation first (see confirmEnableMib); disabling, and any change on
    // a profile, applies immediately.
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

    // validateFilterField parses a textarea into one pattern per line and
    // checks each one with `new RegExp`. It never comma-splits: a pattern
    // like `if{2,4}` is a single valid regex that happens to contain a comma.
    // Returns the parsed lines, or null (with an inline error rendered) if
    // any pattern fails to compile.
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

    // ========================================
    // Per-object polling picker
    //
    // GET /api/devices/{id}/mibs/{mibId}/objects answers, for every object of
    // one module, whether this device polls it right now and — when it does
    // not — which rule stopped it. The verdict is produced by the same
    // selection code the poller runs, so nothing here recomputes it: a box is
    // ticked if and only if the server said `polled`. Ticking a box edits the
    // module's filter patterns; the write goes to the ordinary assignment
    // endpoint and the plan is re-fetched afterwards, so the ticks that come
    // back are verdicts rather than a client-side guess at them.
    // ========================================

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
            // The list opens on what can actually be operated. A table-heavy
            // module is mostly columns and unpollable nodes, and burying the
            // ~24 rows an operator configures under 269 they cannot is what
            // made this picker read as broken.
            show: 'actionable',
            sort: 'poller',
            // Where in the rows the reader is. Ticks live in the draft and the
            // intents above, never in the checkbox, so paging away from a row
            // and back to it cannot lose the edit.
            //
            // The picker pages at LIST_PAGE rather than at the MIB browser's
            // MIB_OBJECT_PAGE: a picker row carries a description line, a
            // verdict and a table's columns, so it is a list row like every
            // other 50-row list in the app and not one of the browser's compact
            // grid rows. Rows-per-page raises it for a reader who wants more.
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

    // objectTableFor finds the table an object is covered by. `table_oid` is
    // present on columns; a row (entry) node carries none, so the table is the
    // longest table OID that prefixes it. Either way this only locates a row
    // in the list the server sent — it decides nothing.
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

    // objectParent is the table a column or row entry structurally belongs to.
    // It is deliberately structural — kind and OID, never a polling verdict —
    // so a column keeps its place under its table however the poller happens
    // to be treating it today.
    objectParent(obj) {
        if (obj.kind !== 'column' && obj.kind !== 'entry') return null;
        const table = this.objectTableFor(obj);
        return table && table.oid !== obj.oid ? table : null;
    }

    // buildObjectGroups turns the flat plan into one row per thing an operator
    // configures: a scalar, or a table with its columns hanging off it. The
    // top-level order is a filtered copy of plan.objects, so the poller's own
    // order — the one a max_oids cap is spent in — still reads correctly.
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

        // A column is normally only reachable through its table's walk, so the
        // columns stay folded away. When one is separately operable — its
        // table is excluded and the agent can still be asked for that column
        // directly — folding it away would hide the only control for it.
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

    // objectControl says which control a row gets. It is a reading of the
    // server's verdict, not a second opinion on it:
    //
    //   toggle    — the object is polled, or a filter this device owns is what
    //               stopped it. Ticking or unticking it means something, so it
    //               gets a working checkbox.
    //   via-table — its table is walked and one walk already returns it. The
    //               object IS collected, so the row reads on; the control that
    //               governs it is the table's.
    //   never     — no filter can make the agent return it.
    //   paused    — the module is off, so no object has a verdict yet.
    //
    // An unrecognised reason gets a working checkbox: filters are the only
    // lever the picker has, so offering it is better than a dead row.
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

    // objectVerdictHtml explains a row's state. `covered-by-table-walk` is the
    // one that used to contradict itself: the row said "Collected" beside an
    // empty box. It is collected, so the row now reads on, and the only
    // control that can stop it — its table's — is named and offered here.
    // `grouped` says the row is already sitting under its table, which carries
    // the explanation and the control, so the row itself only has to say what
    // state it is in.
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

    // maxOidsNeededFor is the cap an object needs to survive: how many objects
    // at or before it, in the server's own order, either are polled or were
    // capped. It counts the server's verdicts rather than re-deciding any of
    // them, and the true answer still arrives with the next fetch.
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

    // visibleGroups applies the search and the display order to a copy of the
    // grouped rows. The server's array is never sorted in place: its order is
    // the one a max_oids cap was spent in, so re-ordering it would change
    // which objects look capped.
    //
    // It also reports what the Show filter is holding back, because a list
    // that quietly drops 269 of 293 rows has to say so.
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

        // The Show filter and the search box both narrow the same set inside
        // visibleGroups(), and the pager then steps through what they left — so
        // the position and the counts describe the rows on screen, never the
        // module's raw total, which the status line names separately.
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

    // renderObjectPickerRow paints one object. A row only carries a checkbox
    // when ticking it would mean something; everything else gets a state marker
    // that agrees with the verdict beside it. There is no disabled checkbox in
    // this list, because a box that cannot be operated is not a control.
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

        // The search box already matches on DESCRIPTION, so the row has to be
        // able to show why it matched. One clipped line, full text in the
        // sheet — the same treatment the MIB browser and the chart cards get.
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

    // objectPickerHowToHtml states the picker's current state and the actions
    // it offers, counted from the server's verdicts. The "nothing is tickable"
    // case is the important one: it is the normal state of an unfiltered
    // module, and it has to read as a fact rather than as a broken control.
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

        // What this picker can do right now, said out loud. Unfiltered, every
        // pollable object is already polled and the only available action is to
        // remove one — correct, but invisible unless it is stated, and hunting
        // for a control that cannot exist is what made this feel broken.
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

    // renderEffectiveMibRow renders one read-only row for the device's "All
    // applied MIBs" list: the effective enabled state plus whether it came
    // from a direct assignment or an assigned profile. Only a direct
    // assignment is editable on the device page, so a profile-sourced row
    // points at the profile instead of offering controls that would not
    // take effect.
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

    // generateDeviceDashboard opens a device's dashboard and reports what its
    // MIBs will contribute.
    //
    // This used to render the generated configuration itself, into a
    // ".metrics-grid" the dashboard redesign removed. The first statement of that
    // renderer therefore threw on a null element, every time — after the view had
    // already switched. The symptom was exactly what it looked like: the button
    // takes you to the dashboard and the device's MIBs are not there.
    //
    // Rendering now goes through the device selector, like openFleetDevice, so
    // the one place that knows how to build a device dashboard stays the only
    // place that does it. What this endpoint uniquely offers is the diagnosis:
    // which MIBs contributed panels, and why the others did not.
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

            // A panel exists only for an object Prometheus already holds data
            // for, so a MIB assigned a moment ago contributes nothing until the
            // device has been polled and answered. Saying which case applies is
            // the difference between "wait a minute" and "this will never work".
            const warnings = dashboardConfig.warnings || [];
            if (warnings.length > 0) {
                warnings.forEach(warning => this.showNotification(warning, 'warning', 15000));
            } else {
                const count = (dashboardConfig.charts || []).length;
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

    updateDashboardCharts(query, metrics, timestamp) {
        console.log('updateDashboardCharts called:', { query, metricsCount: metrics?.length, timestamp });

        // Skip if no metrics
        if (!metrics || !Array.isArray(metrics) || metrics.length === 0) {
            console.log('No metrics to update, skipping');
            return;
        }

        const time = new Date(timestamp * 1000);
        console.log('Active subscriptions count:', this.activeSubscriptions.size);

        // Live updates go to the subscribed charts. The branch that used to sit
        // here fed the charts showDeviceDashboard built, which no longer exist.
        // Iterate through all active subscriptions and update matching elements
        this.activeSubscriptions.forEach((subscriptionData, dashboardKey) => {
            const { metricQueries, statusQueries, objects } = subscriptionData;

            console.log('Checking subscription:', { dashboardKey, query, hasMetricQueries: !!metricQueries, hasStatusQueries: !!statusQueries });

            // Check if this query matches any of our subscriptions
            // Try exact match first, then try all stored queries
            let isMetric = metricQueries && metricQueries.has(query);
            let isStatus = statusQueries && statusQueries.has(query);
            let matchedQuery = query;

            // If no exact match, try finding a matching query
            if (!isMetric && !isStatus) {
                // Check all stored queries
                for (const storedQuery of [...(metricQueries || []), ...(statusQueries || [])]) {
                    if (storedQuery === query) continue; // already checked

                    // Simple check: if queries share same metric name and device
                    const queryMetric = this.queryMetricName(query);
                    const queryDevice = query.match(/device="([^"]+)"/)?.[1];
                    const storedMetric = this.queryMetricName(storedQuery);
                    const storedDevice = storedQuery.match(/device="([^"]+)"/)?.[1];

                    if (queryMetric && storedMetric && queryDevice && storedDevice &&
                        queryMetric === storedMetric && queryDevice === storedDevice) {
                        isMetric = metricQueries && metricQueries.has(storedQuery);
                        isStatus = statusQueries && statusQueries.has(storedQuery);
                        matchedQuery = storedQuery;
                        console.log('Found matching query by metric/device:', { query, matchedQuery: storedQuery });
                        break;
                    }
                }
            }

            console.log('Query match result:', { isMetric, isStatus, query, matchedQuery });

            if (!isMetric && !isStatus) {
                console.log('Query does not match this subscription, skipping');
                return;
            }

            // Get the object info for this query
            const obj = objects.get(matchedQuery);
            if (!obj) return;

            // Parse dashboard key: "deviceId-mibName"
            // MIB names can contain hyphens (e.g., "IF-MIB"), so split on first hyphen only
            const firstHyphenIndex = dashboardKey.indexOf('-');
            const deviceId = dashboardKey.substring(0, firstHyphenIndex);
            const mibName = dashboardKey.substring(firstHyphenIndex + 1);

            console.log(`Updating ${isStatus ? 'status' : 'chart'} for ${obj.name} from query: ${query}`);

            if (isStatus) {
                // Update status text element
                const elementId = obj.statusId || `status-${deviceId}-${mibName}-${obj.name}`;
                const statusElement = document.getElementById(elementId);

                if (statusElement) {
                    let actualValue;

                    // Check if this is an info metric (string status field)
                    if (obj.isInfoMetric && metrics[0]?.metric?.value) {
                        // For info metrics, the value is in the "value" label
                        actualValue = metrics[0].metric.value;
                        console.log(`Extracted info metric value from label: ${actualValue}`);
                    } else {
                        // Extract actual value from metric value (numeric status fields)
                        actualValue = metrics[0]?.value || 'N/A';
                        if (typeof actualValue === 'object' && actualValue !== null) {
                            // For boolean/enum objects, extract the numeric or string value
                            if ('value' in actualValue) {
                                actualValue = actualValue.value;
                            } else if ('Value' in actualValue) {
                                actualValue = actualValue.Value;
                            } else {
                                // Try to get first property value
                                const keys = Object.keys(actualValue);
                                if (keys.length > 0) {
                                    actualValue = actualValue[keys[0]];
                                } else {
                                    actualValue = String(actualValue);
                                }
                            }
                        }
                    }

                    // Get enum map from data attribute
                    let enumData = {};
                    try {
                        const enumAttr = statusElement.getAttribute('data-enum');
                        if (enumAttr) {
                            enumData = JSON.parse(enumAttr);
                        }
                    } catch (e) {
                        console.warn('Failed to parse enum data:', e);
                    }
                    // A sample after the timeout replaces the terminal state:
                    // the tile reported what was known, not a prophecy.
                    this.clearStatusNoData(statusElement);

                    // Try to map value to enum string
                    if (enumData && Object.keys(enumData).length > 0) {
                        const enumKey = String(Math.floor(actualValue));
                        statusElement.textContent = enumData[enumKey] || String(actualValue);
                    } else {
                        statusElement.textContent = this.formatStatusValue(actualValue, obj.type);
                    }
                    console.log(`Updated status ${elementId} with value: ${actualValue}`);
                }
            } else {
                // Update chart
                const chartId = obj.chartId || `chart-${deviceId}-${mibName}-${obj.name}`;
                console.log(`Looking for chart with ID: ${chartId}`);
                console.log(`  Available charts (${Object.keys(this.charts).length}):`, Object.keys(this.charts));
                console.log(`  Chart exists:`, !!this.charts[chartId]);
                const chart = this.charts[chartId];

                if (chart) {
                    metrics.forEach(m => {
                        const option = chart.getOption();

                        // The first sample turns the placeholder card into a chart.
                        if (!chart._hasData) {
                            this.revealChart(chartId);
                            chart._hasData = true;
                            chart._integerValued = !obj.useRate && Number.isInteger(m.value);
                            chart._timeBand = netmonCharts.timeBand(0);
                            chart._spanMs = 0;
                            chart._sparse = true;
                            chart.setOption(this.buildSeriesOption(chart, obj, [[time, m.value]]), true);
                            console.log(`Initialized chart ${chartId} with first data point`);
                        } else {
                            // Append to existing data
                            if (option.series && option.series[0]) {
                                const data = option.series[0].data || [];
                                data.push([time, m.value]);

                                // Keep only last 200 points for performance
                                if (data.length > 200) {
                                    data.shift();
                                }

                                // Throttle chart updates - only update every 2 seconds
                                const now = Date.now();
                                const lastUpdate = chart._lastUpdate || 0;
                                if (now - lastUpdate > 2000) {
                                    const patch = this.chartUpdate(chart, obj, data, m.value);
                                    chart.setOption(patch, false, false); // notMerge=false, lazyUpdate=false for efficiency
                                    chart._lastUpdate = now;
                                    console.log(`Updated chart ${chartId} with new data point`);
                                }
                            }
                        }
                    });
                }
            }
        });
    }

    // ========================================
    // Chart option construction
    //
    // Every metric card is one series over time, so there is exactly one
    // builder for it. Colours and date formats come from netmonCharts, which
    // reads the CSS design tokens, so nothing here needs to know the theme.
    // ========================================

    buildSeriesOption(chart, obj, data) {
        const useRate = obj.useRate || false;
        const spanMs = this.seriesSpan(data);

        return {
            tooltip: {
                trigger: 'axis',
                axisPointer: { type: 'line' },
                formatter: (params) => {
                    if (!params || params.length === 0) return '';
                    const param = params[0];
                    const when = netmonCharts.timestampLabel(param.value[0], chart._spanMs || 0);
                    const value = this.formatMetricValue(param.value[1], obj.name, useRate);
                    return `${when}<br/>${obj.name}: <b>${value}</b>`;
                }
            },
            grid: { left: 4, right: 14, top: 18, bottom: 2, containLabel: true },
            xAxis: this.timeAxisOption(spanMs, data),
            yAxis: this.valueAxisOption(chart, obj),
            series: [{
                type: 'line',
                smooth: true,
                symbol: 'circle',
                symbolSize: 5,
                // A line through one point draws nothing, so a lone sample is
                // shown as the point it is.
                showSymbol: data.length < 2,
                data: data,
                lineStyle: { width: 1.75 },
                areaStyle: netmonCharts.areaStyle(chart, 0)
            }]
        };
    }

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

        // With a single sample the data extent is a point, and ECharts widens
        // it to something arbitrary — often days — which the labels then have
        // to describe. Pin a minute around the sample instead. Once a second
        // sample lands, null hands the extent back to the data.
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

    valueAxisOption(chart, obj) {
        const useRate = obj.useRate || false;
        return {
            type: 'value',
            name: this.getMetricUnit(obj.name, useRate),
            nameLocation: 'end',
            nameGap: 10,
            // An integer series must not be split into fractional ticks, or the
            // labels round to the same string and the axis repeats itself.
            minInterval: chart._integerValued ? 1 : 0,
            axisLabel: {
                hideOverlap: true,
                formatter: (value) => this.formatAxisValue(value, obj.name)
            }
        };
    }

    seriesSpan(data) {
        if (!data || data.length < 2) return 0;
        const first = new Date(data[0][0]).getTime();
        const last = new Date(data[data.length - 1][0]).getTime();
        return Math.max(0, last - first);
    }

    // Carries the new data plus only the axis options that have to change with
    // it, so a steady-state append stays a series-only patch.
    chartUpdate(chart, obj, data, latestValue) {
        const patch = { series: [{ data: data }] };

        const spanMs = this.seriesSpan(data);
        chart._spanMs = spanMs;

        const band = netmonCharts.timeBand(spanMs);
        const sparse = data.length < 2;
        if (band !== chart._timeBand || sparse !== chart._sparse) {
            chart._timeBand = band;
            chart._sparse = sparse;
            patch.xAxis = this.timeAxisOption(spanMs, data);
            patch.series[0].showSymbol = sparse;
        }

        if (chart._integerValued && !Number.isInteger(latestValue)) {
            chart._integerValued = false;
            patch.yAxis = this.valueAxisOption(chart, obj);
        }

        return patch;
    }

    // ========================================
    // Card data states
    //
    // A card is either waiting, charting, or terminal. The state lives on the
    // container so the placeholder costs a couple of text lines rather than a
    // full-height empty plot frame.
    // ========================================

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

    // A status tile is the same three states as a chart card in one line of
    // text: waiting, showing a value, or terminal. It used to have only the
    // first two, so an object the device never answered for sat on "Loading..."
    // for the life of the page — and then had its tile hidden outright, which
    // is not a state anybody can read.
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

    // A card whose widget the server could not name a metric for. This is a
    // terminal state, not a wait: nothing will ever arrive for it.
    markChartSpecError(chartId, detail) {
        const card = this.chartCard(chartId);
        if (!card) return;

        card.classList.add('is-empty', 'is-nodata');
        const message = card.querySelector('.chart-empty-title');
        const message2 = card.querySelector('.chart-empty-detail');
        if (message) message.textContent = 'Unavailable';
        if (message2) message2.textContent = detail;
    }

    markStatusSpecError(statusId, detail) {
        const statusElement = document.getElementById(statusId);
        if (statusElement) statusElement.textContent = detail;
    }

    // ========================================
    // METRIC NAMES AND QUERIES
    // ========================================
    // The exporter names a series after the MIB object it instantiates and
    // carries the row index in labels, so a metric name is no longer derivable
    // from an instance OID. internal/webui/dashboard/generator.go resolves the
    // name once, server side, and ships it on every widget;
    // internal/promname/contract_test.go pins that resolution to what the
    // exporter actually emits. The frontend's job is to use what it is given.

    // escapeLabelValue escapes a value for a PromQL label matcher.
    escapeLabelValue(value) {
        return String(value).replace(/\\/g, '\\\\').replace(/"/g, '\\"');
    }

    // metricQuery builds `name{device="…",label="…"}`. Selecting by name rather
    // than by a __name__ regex is the point of the change: a regex matcher has
    // to be tested against every metric name in the TSDB, which is what made
    // the old OID-derived queries slow.
    metricQuery(metric, deviceName, matchers) {
        const parts = [`device="${this.escapeLabelValue(deviceName)}"`];

        for (const [label, value] of Object.entries(matchers || {})) {
            if (!label || value === undefined || value === null || value === '') continue;
            parts.push(`${label}="${this.escapeLabelValue(value)}"`);
        }

        return `${metric}{${parts.join(',')}}`;
    }

    // queryMetricName reads the metric name back out of a query. Queries come
    // in two shapes: a plain name with matchers, and the __name__ matcher still
    // used for a raw OID no loaded MIB describes. Both have to be recognised or
    // the subscription dedup silently stops matching anything.
    queryMetricName(query) {
        const byNameMatcher = query.match(/__name__=~?"([^"]+)"/);
        if (byNameMatcher) return byNameMatcher[1];

        // Start of string or just inside a function call, e.g. rate(snmp_x{…}).
        const bareName = query.match(/(?:^|[\s(])([a-zA-Z_:][\w:]*)\s*\{/);
        return bareName ? bareName[1] : null;
    }

    // specMetric reads the metric name the server resolved for a widget. A
    // widget without one means the spec and the exporter have drifted or a
    // stale spec was cached. Either way it is reported rather than guessed at:
    // falling back to an OID-derived name client side would hide exactly the
    // drift the server-side contract test exists to catch.
    specMetric(spec, context) {
        const metric = spec && spec.metric;
        if (typeof metric === 'string' && metric !== '') return metric;

        if (!this.reportedMissingMetrics.has(context)) {
            this.reportedMissingMetrics.add(context);
            console.error(
                `Dashboard spec has no metric name for ${context}. The server resolves ` +
                `metric names and the frontend will not guess one; reload to discard a ` +
                `cached spec.`, spec);
        }
        return null;
    }

    // Text shown in a card or status field whose widget could not be named.
    static get MISSING_METRIC_DETAIL() {
        return 'Dashboard spec has no metric name for this object.';
    }

    // instanceMatchers pins a query to the row the instance selector chose.
    // The selector offers one column's value, so that column's label is what
    // can be matched on; index_labels tells us whether that identifies a row
    // exactly or only as far as that column is unique within the table.
    instanceMatchers(instanceSelector, selectedInstance) {
        const labelName = instanceSelector && instanceSelector.label_name;
        if (!labelName || !selectedInstance) return {};
        return { [labelName]: selectedInstance };
    }

    isSystemInfoOID(oid) {
        // Normalize OID by adding .0 suffix if not present
        const normalizedOid = oid.endsWith(".0") ? oid : oid + ".0";
        
        const systemInfoOIDs = [
            "1.3.6.1.2.1.1.1.0",  // sysDescr
            "1.3.6.1.2.1.1.2.0",  // sysObjectID
            "1.3.6.1.2.1.1.4.0",  // sysContact
            "1.3.6.1.2.1.1.5.0",  // sysName
            "1.3.6.1.2.1.1.6.0",  // sysLocation
        ];
        return systemInfoOIDs.includes(normalizedOid);
    }

    // ========================================
    // Fetch system info from the API
    // ========================================
    async fetchSystemInfo(deviceName) {
        try {
            const response = await fetch('/api/metrics/system-info', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ device: deviceName })
            });

            if (!response.ok) {
                console.error('Failed to fetch system info');
                return null;
            }

            const data = await response.json();
            return data.values || {};
        } catch (error) {
            console.error('Error fetching system info:', error);
            return null;
        }
    }

    oidToSystemInfoLabel(oid) {
        // Normalize OID by adding .0 suffix if not present
        const normalizedOid = oid.endsWith(".0") ? oid : oid + ".0";
        
        const mapping = {
            "1.3.6.1.2.1.1.1.0": "sys_descr",
            "1.3.6.1.2.1.1.2.0": "sys_object_id",
            "1.3.6.1.2.1.1.4.0": "sys_contact",
            "1.3.6.1.2.1.1.5.0": "sys_name",
            "1.3.6.1.2.1.1.6.0": "sys_location",
        };
        return mapping[normalizedOid] || null;
    }

    // ========================================
    // Fetch and display system info for widgets
    // ========================================
    async fetchAndDisplaySystemInfo(deviceId, mibName, deviceName, systemInfoWidgets) {
        console.log(`Fetching system info for device ${deviceName}`);
        
        const systemInfo = await this.fetchSystemInfo(deviceName);
        
        if (!systemInfo) {
            console.error('Failed to fetch system info');
            // Mark all system info widgets as "No Data"
            for (const widget of systemInfoWidgets) {
                const statusId = `status-${deviceId}-${mibName}-${widget.name}`;
                const statusElement = document.getElementById(statusId);
                if (statusElement) {
                    statusElement.textContent = 'No Data';
                    statusElement.style.color = '#64748b';
                }
            }
            return;
        }

        console.log('System info received:', systemInfo);

        // Populate status fields with system info values
        for (const widget of systemInfoWidgets) {
            const statusId = `status-${deviceId}-${mibName}-${widget.name}`;
            const statusElement = document.getElementById(statusId);
            
            if (statusElement) {
                const labelName = this.oidToSystemInfoLabel(widget.oid);
                const value = systemInfo[labelName];
                
                if (value) {
                    statusElement.textContent = value;
                    statusElement.style.color = ''; // Reset to default color
                    console.log(`Set ${statusId} = ${value}`);
                } else {
                    statusElement.textContent = 'N/A';
                    statusElement.style.color = '#64748b';
                }
            }
        }
    }


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

    formatStatusValue(value, type) {
        if (!type) return value.toString();

        if (type.includes('TimeTicks')) {
            // Convert timeticks to human readable
            const totalSeconds = Math.floor(value / 100);
            const days = Math.floor(totalSeconds / 86400);
            const hours = Math.floor((totalSeconds % 86400) / 3600);
            const minutes = Math.floor((totalSeconds % 3600) / 60);
            return `${days}d ${hours}h ${minutes}m`;
        }

        if (type.includes('PhysAddress') || type.includes('IpAddress')) {
            // These might need special formatting depending on backend representation
            return value.toString();
        }

        return value.toString();
    }

    // ========================================
    // Helper: Format metric values for tooltips
    // ========================================
    formatMetricValue(value, name, useRate = false) {
        if (value === null || value === undefined) return 'N/A';

        const numValue = parseFloat(value);
        if (isNaN(numValue)) return value.toString();

        const nameLower = name ? name.toLowerCase() : '';
        const rateSuffix = useRate ? '/s' : '';

        // Only format bytes/octets as KB/MB/GB
        if (nameLower.includes('octets') || nameLower.includes('bytes')) {
            if (numValue >= 1073741824) {
                return (numValue / 1073741824).toFixed(2) + ' GB' + rateSuffix;
            } else if (numValue >= 1048576) {
                return (numValue / 1048576).toFixed(2) + ' MB' + rateSuffix;
            } else if (numValue >= 1024) {
                return (numValue / 1024).toFixed(2) + ' KB' + rateSuffix;
            }
            return numValue.toFixed(0) + ' B' + rateSuffix;
        }

        // For all other metrics, show plain numbers with thousand separators
        if (numValue >= 1000) {
            return numValue.toLocaleString('en-US', { maximumFractionDigits: 0 }) + (useRate ? '/s' : '');
        }

        return numValue.toFixed(2) + (useRate ? '/s' : '');
    }

    // ========================================
    // Helper: Format axis values (compact format)
    // ========================================
    // Ticks carry magnitude only. The unit is stated once, by the axis name,
    // rather than repeated on every label.
    formatAxisValue(value, name) {
        if (value === null || value === undefined) return '';

        const numValue = parseFloat(value);
        if (isNaN(numValue)) return value.toString();

        const nameLower = name ? name.toLowerCase() : '';

        // Only format bytes/octets as K/M/G
        if (nameLower.includes('octets') || nameLower.includes('bytes')) {
            if (numValue >= 1073741824) {
                return (numValue / 1073741824).toFixed(1) + 'G';
            } else if (numValue >= 1048576) {
                return (numValue / 1048576).toFixed(1) + 'M';
            } else if (numValue >= 1024) {
                return (numValue / 1024).toFixed(1) + 'K';
            }
            return netmonCharts.formatTick(numValue);
        }

        // For other metrics, use compact notation
        if (numValue >= 1000000) {
            return (numValue / 1000000).toFixed(1) + 'M';
        } else if (numValue >= 1000) {
            return (numValue / 1000).toFixed(1) + 'K';
        }

        // Below the compact thresholds the tick has to carry enough precision
        // that neighbouring ticks cannot round to the same string.
        return netmonCharts.formatTick(numValue);
    }

    // ========================================
    // Helper: Get metric unit label for Y-axis
    // ========================================
    getMetricUnit(name, useRate = false) {
        if (!name) return '';

        const nameLower = name.toLowerCase();
        const rateSuffix = useRate ? '/s' : '';

        // Detect bytes/octets metrics
        if (nameLower.includes('octets') || nameLower.includes('bytes')) {
            return useRate ? 'Bytes/s' : 'Bytes';
        }

        // Detect packet metrics
        if (nameLower.includes('pkts') || nameLower.includes('packets')) {
            return useRate ? 'Packets/s' : 'Packets';
        }

        // Detect speed/bandwidth metrics
        if (nameLower.includes('speed') || nameLower.includes('bandwidth')) {
            return 'bps';
        }

        return useRate ? '/s' : '';
    }

    // ========================================
    // MIB Browser (existing functionality)
    // ========================================

    async loadDevices() {
        try {
            const response = await fetch('/api/devices');
            const data = await response.json();

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
            this.restoreState();
        } catch (error) {
            console.error('Error loading devices:', error);
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

            // Paged rather than painted whole. The library is 170 modules
            // here and grows with every import; rendering all of them built an
            // accordion whose every item was in the document at once and hid
            // the ones that did not match, so the cost of a search was paid on
            // every keystroke against the entire library.
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

        // Zero is the correct answer for TEXTUAL-CONVENTION and conformance-only
        // modules, so it is described rather than shown as a bare "0 objects".
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

        // The largest module in the library declares 10,539 objects, so the
        // list is searched by name, OID or description and paged — the same two
        // controls every other long list in the app has. Several modules can be
        // expanded at once, so the pager is named after the module it belongs
        // to rather than after "objects", which would not distinguish them.
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

    // Filters the collapsed list by module name. Nothing is expanded and no
    // request is issued.
    // What the MIB browser can be ordered by. Name is the default because it is
    // how a reader looks a module up; object count answers "which of these is
    // worth expanding", and imported date answers "what did I just add".
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


    // The summary reads the list's own state rather than being handed counts,
    // so it cannot disagree with the pager beneath it about how many matched.
    // The pager states position within the matches; this states what the
    // library holds and what the filter did to it.
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

        // Only the current page is in the document, so this expands the page
        // the reader is looking at. That is the bound: expanding is one request
        // per module, and a page is a screenful by construction. Before the
        // list was paged this could be aimed at the whole library at once,
        // which is why the ceiling below exists at all.
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
                this.loadDeviceDashboards(deviceId);
                this.saveState();
            } else {
                // Clearing the selection goes back to the fleet, not to a blank
                // frame asking to be told what to look at.
                this.currentDashboardDevice = null;
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
                this.showNotification('Settings saved!', 'success');
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

    // An import runs twice: once to plan and once to apply.
    //
    // The endpoint defaults to a dry run and only applies with ?apply=true,
    // which is what makes this possible — the confirmation quotes what the
    // server said it would do to this instance rather than asking the operator
    // to agree to a file name. A bundle that matches what is already here
    // reports no changes and never reaches the second call.
    async importConfiguration(file, button) {
        button.disabled = true;
        this.configTransferStatus(`Checking ${file.name}…`, null);

        try {
            const plan = await this.postConfigBundle(file, false);

            // report.changed counts devices and profiles only, so a bundle
            // whose devices already match but which carries modules this
            // instance does not have still has work to do. Refreshing modules
            // that are already present is the one genuinely empty case.
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

            // Everything on screen was built from the configuration that has
            // just been replaced, so it is reloaded rather than left showing
            // the previous instance's devices.
            await this.loadDevices();
            await this.loadProfiles();
            await this.loadStoredMIBs();
            this.showFleetOverview();
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

    async fetchAndDisplayDeviceStatus(deviceId) {
        const banner = document.getElementById('deviceStatusBanner');

        try {
            const response = await fetch(`/api/devices/${deviceId}/status`);

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

    async loadDeviceDashboards(deviceId) {
        try {
            // First, fetch device status to check for any errors
            await this.fetchAndDisplayDeviceStatus(deviceId);

            // Fetch device MIBs (includes both direct and profile MIBs)
            const response = await fetch(`/api/devices/${deviceId}/mibs`);
            const data = await response.json();

            // Use all_mibs which includes both direct and profile MIBs
            let mibs = data.all_mibs || [];

            // Filter to only show MIBs with available data
            const filteredResponse = await fetch('/api/mibs?filter=available');
            const filteredData = await filteredResponse.json();
            const availableMibNames = new Set((filteredData.mibs || []).map(m => m.name));

            // Only show MIBs that have available data
            mibs = mibs.filter(mib => availableMibNames.has(mib.name));

            if (!mibs || mibs.length === 0) {
                document.getElementById('dashboardContent').innerHTML = `
                    <div class="empty-state">
                        <h3>No MIBs Assigned</h3>
                        <p>This device has no MIBs assigned. Go to the device details page to assign MIBs.</p>
                    </div>
                `;
                document.getElementById('mibTabsContainer').style.display = 'none';
                return;
            }

            // Store device MIBs
            this.deviceDashboards[deviceId] = {
                mibs: mibs,
                dashboards: {}
            };

            // Hide empty state, show tabs container
            document.getElementById('dashboardContent').style.display = 'none';
            document.getElementById('mibTabsContainer').style.display = 'block';

            // Create tabs for each MIB
            this.createMibTabs(deviceId, mibs);

            // Load ALL MIB dashboards sequentially to avoid race conditions
            if (mibs.length > 0) {
                this.activeMibTab = mibs[0].name;
                // Load MIBs sequentially to prevent tab content from getting mixed up
                this.loadMibChartsSequentially(deviceId, mibs, 0);
            }
        } catch (error) {
            console.error('Error loading device dashboards:', error);
            this.showNotification('Failed to load device dashboards', 'error');
        }
    }

    async loadMibChartsSequentially(deviceId, mibs, index) {
        // Base case: all MIBs loaded
        if (index >= mibs.length) {
            return;
        }

        // Load current MIB
        const mib = mibs[index];
        await this.loadMibCharts(deviceId, mib.name);

        // Load next MIB
        await this.loadMibChartsSequentially(deviceId, mibs, index + 1);
    }

    // createMibTabs builds the module rail and one pane per module.
    //
    // The rail is a filterable column, not a row of tabs: a device can carry
    // hundreds of assigned modules, and a horizontal strip has exactly one
    // screen of room to spend before it starts hiding names off the right edge.
    // A column has a scrollbar and a filter box instead, so the cost of the
    // 200th module is one more row in a list rather than one more thing that
    // cannot be reached.
    //
    // The server's order is kept exactly as it arrives. It is already
    // alphabetical, and sorting here would make a module's position depend on
    // the client rather than on the payload.
    createMibTabs(deviceId, mibs) {
        const tabList = document.getElementById('mibTabList');
        const tabContent = document.getElementById('mibTabContent');

        // Clear existing tabs
        tabList.innerHTML = '';
        tabContent.innerHTML = '';

        mibs.forEach((mib, index) => {
            tabList.appendChild(this.mibTabButton(deviceId, mib.name, index === 0));

            const tabPane = document.createElement('div');
            tabPane.className = 'tab-pane' + (index === 0 ? ' active' : '');
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

    // filterMibTabs narrows the rail. It never changes the selection: hiding a
    // row is a way to reach some other module, not a way to close the one being
    // read, so the selected pane stays on screen even while its row is filtered
    // away.
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

    // Arrow keys move focus along the rail; Enter or Space then opens the
    // module. Focus and selection are kept apart deliberately — opening a
    // module fetches and renders a dashboard, which is far too much work to do
    // once per keypress while someone is scanning the list.
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

        // Load charts if not already loaded (lazy loading)
        const deviceDashboard = this.deviceDashboards[deviceId];
        if (!deviceDashboard.dashboards[mibName]) {
            this.loadMibCharts(deviceId, mibName);
        }

        // CRITICAL FIX: Resize all charts in the newly visible tab
        // ECharts needs to be resized when its container becomes visible
        // Use multiple resize attempts with increasing delays to handle layout timing
        this.resizeChartsInTab(mibName);
    }

    resizeChartsInTab(mibName) {
        const activeTabPane = document.getElementById(`mib-tab-${mibName}`);
        if (!activeTabPane) return;

        const doResize = () => {
            console.log(`Resizing charts for MIB tab: ${mibName}`);
            const chartElements = activeTabPane.querySelectorAll('[id^="chart-"]');
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

    // ========================================
    // REFACTORED: Dashboard rendering from DashboardSpec
    // Old functions removed: fetchTableMetadata(), isTableColumn()
    // ========================================

    async loadMibCharts(deviceId, mibName) {
        try {
            console.log(`Loading MIB charts for ${mibName} on device ${deviceId}`);

            const tabPane = document.getElementById(`mib-tab-${mibName}`);
            if (!tabPane) return;

            // Show loading state
            tabPane.innerHTML = '<div class="charts-loading">Loading dashboard...</div>';

            // Get the MIB from cached device dashboards
            const deviceDash = this.deviceDashboards[deviceId];
            if (!deviceDash || !deviceDash.mibs) {
                tabPane.innerHTML = '<div class="empty-state"><p>Device data not loaded</p></div>';
                return;
            }

            // Find the MIB in the cached list
            const mib = deviceDash.mibs.find(m => m.name === mibName);
            if (!mib) {
                tabPane.innerHTML = '<div class="empty-state"><p>MIB not found</p></div>';
                return;
            }

            // Fetch dashboard specification from backend
            console.log(`Fetching dashboard spec for ${mibName} (ID: ${mib.id})`);
            const dashboardResponse = await fetch(`/api/mibs/${mib.id}/dashboard`);
            if (!dashboardResponse.ok) {
                tabPane.innerHTML = '<div class="empty-state"><p>Failed to load dashboard specification</p></div>';
                return;
            }

            const dashboardSpec = await dashboardResponse.json();
            console.log(`Loaded dashboard spec for ${mibName}:`, dashboardSpec);

            if ((!dashboardSpec.scalar_widgets || dashboardSpec.scalar_widgets.length === 0) &&
                (!dashboardSpec.table_widgets || dashboardSpec.table_widgets.length === 0)) {
                tabPane.innerHTML = '<div class="empty-state"><p>No displayable objects found in this MIB</p></div>';
                return;
            }

            // Get device info for queries
            const deviceResponse = await fetch(`/api/devices/${deviceId}`);
            const device = await deviceResponse.json();

            // Render dashboard from spec
            await this.renderDashboardFromSpec(deviceId, mibName, mib.id, device, dashboardSpec);

            // Store that this dashboard has been loaded
            if (!this.deviceDashboards[deviceId].dashboards[mibName]) {
                this.deviceDashboards[deviceId].dashboards[mibName] = {
                    spec: dashboardSpec
                };
            }
        } catch (error) {
            console.error('Error loading MIB charts:', error);
            const tabPane = document.getElementById(`mib-tab-${mibName}`);
            if (tabPane) {
                tabPane.innerHTML = '<div class="empty-state"><p>Error loading charts</p></div>';
            }
        }
    }

    // One metric card. The chart is the card: the object name labels it and the
    // MIB DESCRIPTION is reference text, kept to a single line with the full
    // wording on hover so it can never push the plot down the card.
    chartCardMarkup(chartId, name, description) {
        // The single line stays: it is what makes every plot in a row start at
        // the same height. What changes is that the line is now a control, so
        // the wording it cannot fit is readable in the description sheet
        // instead of only in a tooltip.
        const caption = this.descriptionTriggerHtml(description, {
            title: name,
            eyebrow: 'Metric',
            className: 'chart-description'
        });

        return `
            <div class="chart-container is-empty">
                <h4 title="${this.escapeAttr(name)}">${this.escapeHtml(name)}</h4>
                ${caption}
                <div id="${chartId}" class="chart-canvas"></div>
                <div class="chart-empty">
                    <span class="chart-empty-title">Waiting for data…</span>
                    <span class="chart-empty-detail">Listening for the first sample.</span>
                </div>
            </div>
        `;
    }

    async renderDashboardFromSpec(deviceId, mibName, mibId, device, dashboardSpec) {
        const tabPane = document.getElementById(`mib-tab-${mibName}`);
        if (!tabPane) return;

        let html = '';

        // Render scalar widgets
        if (dashboardSpec.scalar_widgets && dashboardSpec.scalar_widgets.length > 0) {
            // Separate widgets by type
            const statusWidgets = dashboardSpec.scalar_widgets.filter(w => w.type === 'status');
            const chartWidgets = dashboardSpec.scalar_widgets.filter(w => w.type === 'chart');

            // Render status widgets
            if (statusWidgets.length > 0) {
                html += '<div class="status-info-section">';
                html += '<h4>Status & Information</h4>';
                html += '<div class="status-grid">';

                for (const widget of statusWidgets) {
                    const statusId = `status-${deviceId}-${mibName}-${widget.name}`;
                    const enumData = JSON.stringify(widget.enum_values || {}).replace(/'/g, '&apos;');
                    const initTime = Date.now();
                    html += `
                        <div class="status-item">
                            <label>${widget.name}:</label>
                            <span id="${statusId}" class="status-value" data-enum='${enumData}' data-init-time='${initTime}'>Loading...</span>
                            ${this.descriptionTriggerHtml(widget.description, {
                                title: widget.name,
                                eyebrow: 'Status object',
                                className: 'status-description'
                            })}
                        </div>
                    `;
                    // Same window the chart cards use, so a silent object
                    // settles at the same moment whichever shape it is in.
                    setTimeout(() => {
                        this.checkStatusDataTimeout(statusId);
                    }, FIRST_SAMPLE_TIMEOUT);
                }

                html += '</div></div>';
            }

            // Render scalar chart widgets
            if (chartWidgets.length > 0) {
                html += '<div class="charts-grid">';

                for (const widget of chartWidgets) {
                    const chartId = `chart-${deviceId}-${mibName}-${widget.name}`;
                    html += this.chartCardMarkup(chartId, widget.name, widget.description);
                }

                html += '</div>';
            }
        }


        // Render table widgets
        for (const tableWidget of dashboardSpec.table_widgets || []) {
            const tableName = tableWidget.table_name;
            const masterSelectorId = `master-instance-selector-${deviceId}-${mibName}-${tableName}`;

            html += `<div class="table-widget-section">`;
            html += `<h4>${tableName}</h4>`;

            // Add instance selector
            if (tableWidget.instance_selector) {
                html += `
                    <div class="master-instance-selector-container">
                        <label for="${masterSelectorId}">Instance:</label>
                        <select id="${masterSelectorId}" class="master-instance-selector"
                                data-device-id="${deviceId}"
                                data-mib-name="${mibName}"
                                data-table-name="${tableName}">
                            <option value="">Loading instances...</option>
                        </select>
                    </div>
                `;
            }

            // Add status fields
            if (tableWidget.status_fields && tableWidget.status_fields.length > 0) {
                html += '<div class="status-grid">';
                for (const field of tableWidget.status_fields) {
                    const statusId = `status-${deviceId}-${mibName}-${tableName}-${field.name}`;
                    const enumData = JSON.stringify(field.enum_values || {}).replace(/'/g, '&apos;');
                    html += `
                        <div class="status-item">
                            <label>${field.name}:</label>
                            <span id="${statusId}" class="status-value" data-enum='${enumData}'>Select an instance above</span>
                            ${this.descriptionTriggerHtml(field.description, {
                                title: field.name,
                                eyebrow: 'Status field',
                                className: 'status-description'
                            })}
                        </div>
                    `;
                }
                html += '</div>';
            }

            // Add metric charts
            if (tableWidget.metric_charts && tableWidget.metric_charts.length > 0) {
                html += '<div class="charts-grid">';
                for (const chart of tableWidget.metric_charts) {
                    const chartId = `chart-${deviceId}-${mibName}-${tableName}-${chart.name}`;
                    html += this.chartCardMarkup(chartId, chart.name, chart.description);
                }
                html += '</div>';
            }

            html += '</div>'; // end table-widget-section
        }

        tabPane.innerHTML = html;

        // Visibility belongs to .tab-pane.active. Every MIB is rendered up
        // front, so forcing an inline display here stacked all of them on the
        // page at once; charts in a hidden pane are sized by resizeChartsInTab
        // when that pane becomes the active one.

        // Force immediate reflow
        void tabPane.offsetHeight;

        // Wait for DOM to update
        await new Promise(resolve => setTimeout(resolve, 100));

        // Subscribe to scalar widgets
        this.subscribeToScalarWidgets(deviceId, mibName, device, dashboardSpec.scalar_widgets || []);

        // Setup instance selectors for table widgets
        for (const tableWidget of dashboardSpec.table_widgets || []) {
            await this.setupTableInstanceSelector(deviceId, mibName, device, tableWidget);
        }

        // Force resize all charts in this tab after setup completes
        // This handles cases where charts were initialized before container had proper dimensions
        this.resizeChartsInTab(mibName);

        // Schedule a check to hide empty MIB tabs after data loading timeout
        setTimeout(() => {
            this.checkAndHideEmptyMibTab(deviceId, mibName);
        }, 35000); // Check after all widget timeouts (30s) + buffer
    }

    checkAndHideEmptyMibTab(deviceId, mibName) {
        const tabPane = document.getElementById(`mib-tab-${mibName}`);
        if (!tabPane) return;

        // Check if tab has any visible content
        const visibleSections = tabPane.querySelectorAll(
            '.status-info-section:not(.no-data-hidden):not([style*="display: none"]), ' +
            '.charts-grid:not(.no-data-hidden):not([style*="display: none"]), ' +
            '.table-widget-section:not(.no-data-hidden):not([style*="display: none"])'
        );

        // Also check if there are visible items within each section
        let hasVisibleContent = false;
        for (const section of visibleSections) {
            const visibleItems = section.querySelectorAll(
                '.chart-container:not(.no-data-hidden):not([style*="display: none"]), ' +
                '.status-item:not(.no-data-hidden):not([style*="display: none"])'
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

    subscribeToScalarWidgets(deviceId, mibName, device, scalarWidgets) {
        const key = `${deviceId}-${mibName}`;
        let subscriptionData = this.activeSubscriptions.get(key);

        if (!subscriptionData) {
            subscriptionData = {
                metricQueries: new Set(),
                statusQueries: new Set(),
                objects: new Map()
            };
            this.activeSubscriptions.set(key, subscriptionData);
        }

        // Separate system info widgets from regular widgets
        const systemInfoWidgets = [];
        const regularWidgets = [];

        for (const widget of scalarWidgets) {
            if (this.isSystemInfoOID(widget.oid)) {
                systemInfoWidgets.push(widget);
            } else {
                regularWidgets.push(widget);
            }
        }

        // Handle regular widgets with WebSocket subscriptions
        for (const widget of regularWidgets) {
            const metric = this.specMetric(widget, `${mibName}.${widget.name}`);
            if (!metric) {
                // Nothing can be subscribed to, so say so on the card rather
                // than leaving it waiting for a sample that cannot arrive.
                if (widget.type === 'status') {
                    this.markStatusSpecError(
                        `status-${deviceId}-${mibName}-${widget.name}`,
                        MonitoringApp.MISSING_METRIC_DETAIL);
                } else if (widget.type === 'chart') {
                    const chartId = `chart-${deviceId}-${mibName}-${widget.name}`;
                    this.initializeEmptyChart(chartId, widget.name);
                    this.markChartSpecError(chartId, MonitoringApp.MISSING_METRIC_DETAIL);
                }
                continue;
            }

            const query = this.metricQuery(metric, device.name);

            if (widget.type === 'status') {
                subscriptionData.statusQueries.add(query);
                subscriptionData.objects.set(query, {
                    ...widget,
                    statusId: `status-${deviceId}-${mibName}-${widget.name}`
                });
            } else if (widget.type === 'chart') {
                subscriptionData.metricQueries.add(query);
                subscriptionData.objects.set(query, {
                    ...widget,
                    chartId: `chart-${deviceId}-${mibName}-${widget.name}`
                });

                // Initialize chart
                const chartId = `chart-${deviceId}-${mibName}-${widget.name}`;
                this.initializeEmptyChart(chartId, widget.name);
            }
        }

        // Handle system info widgets with API fetch
        if (systemInfoWidgets.length > 0) {
            this.fetchAndDisplaySystemInfo(deviceId, mibName, device.name, systemInfoWidgets);
        }

        // Send subscribe message for regular widgets only
        const allQueries = [...subscriptionData.statusQueries, ...subscriptionData.metricQueries];
        if (allQueries.length > 0 && this.ws && this.ws.readyState === WebSocket.OPEN) {
            console.log(`Subscribing to ${allQueries.length} scalar widget queries`);
            this.ws.send(JSON.stringify({
                type: 'subscribe',
                queries: allQueries
            }));
        }
    }

    async setupTableInstanceSelector(deviceId, mibName, device, tableWidget) {
        const tableName = tableWidget.table_name;
        const masterSelectorId = `master-instance-selector-${deviceId}-${mibName}-${tableName}`;
        const masterSelector = document.getElementById(masterSelectorId);

        if (!masterSelector) {
            console.warn(`Master instance selector not found: ${masterSelectorId}`);
            return;
        }

        if (!tableWidget.instance_selector || !tableWidget.instance_selector.query_template) {
            console.warn(`No instance selector query template for ${tableName}, hiding section`);
            const tableWidgetSection = masterSelector.closest('.table-widget-section');
            if (tableWidgetSection) {
                tableWidgetSection.style.display = 'none';
                tableWidgetSection.classList.add('no-data-hidden');
            }
            return;
        }

        // Build query from template
        const queryTemplate = tableWidget.instance_selector.query_template;
        const query = queryTemplate.replace(/\{\{device\}\}/g, device.name);

        console.log(`Querying instances for ${tableName}: ${query}`);

        try {
            const response = await fetch('/api/metrics/instant', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ query: query })
            });

            if (!response.ok) {
                console.error('Failed to query instances, hiding section');
                const tableWidgetSection = masterSelector.closest('.table-widget-section');
                if (tableWidgetSection) {
                    tableWidgetSection.style.display = 'none';
                    tableWidgetSection.classList.add('no-data-hidden');
                }
                return;
            }

            const data = await response.json();
            console.log(`Instance query response:`, data);

            // Extract unique instances from the label
            const labelName = tableWidget.instance_selector.label_name;
            const friendlyNameLabel = tableWidget.instance_selector.friendly_name_label;

            // Use Map to store instance value -> friendly name mapping
            const instanceMap = new Map();

            // Handle API response - backend returns {metrics: [...]} not {result: [...]}
            const metrics = data.metrics || data.result || [];
            if (Array.isArray(metrics)) {
                for (const item of metrics) {
                    const metric = item.metric || item.Metric;
                    if (metric && metric[labelName]) {
                        const instanceValue = metric[labelName];
                        const friendlyName = friendlyNameLabel ? (metric[friendlyNameLabel] || null) : null;

                        // Store or update the mapping
                        if (!instanceMap.has(instanceValue) || friendlyName) {
                            instanceMap.set(instanceValue, friendlyName);
                        }
                    }
                }
            }

            // Convert to array and sort intelligently
            const instanceArray = Array.from(instanceMap.entries()).sort((a, b) => {
                const [valueA, friendlyA] = a;
                const [valueB, friendlyB] = b;

                // If both have friendly names, sort by friendly name
                if (friendlyA && friendlyB) {
                    return friendlyA.localeCompare(friendlyB);
                }

                // If only one has a friendly name, prioritize it
                if (friendlyA && !friendlyB) return -1;
                if (!friendlyA && friendlyB) return 1;

                // Neither has friendly name, try numeric sort first
                const numA = parseInt(valueA, 10);
                const numB = parseInt(valueB, 10);
                if (!isNaN(numA) && !isNaN(numB)) {
                    return numA - numB;
                }

                // Fall back to string comparison
                return String(valueA).localeCompare(String(valueB));
            });

            console.log(`Found ${instanceArray.length} instances for ${tableName}:`, instanceArray);

            // If no instances found, hide the entire table widget section
            if (instanceArray.length === 0) {
                console.log(`No instances found for ${tableName}, hiding table widget section`);
                const tableWidgetSection = masterSelector.closest('.table-widget-section');
                if (tableWidgetSection) {
                    tableWidgetSection.style.display = 'none';
                    tableWidgetSection.classList.add('no-data-hidden');
                }
                return;
            }

            // Populate selector with friendly names if available
            masterSelector.innerHTML = '<option value="">Select instance...</option>';
            for (const [instanceValue, friendlyName] of instanceArray) {
                const displayText = friendlyName
                    ? `${this.escapeHtml(friendlyName)} (${this.escapeHtml(instanceValue)})`
                    : this.escapeHtml(instanceValue);
                masterSelector.innerHTML += `<option value="${this.escapeHtml(instanceValue)}">${displayText}</option>`;
            }

            // Auto-select first instance automatically
            if (instanceArray.length >= 1) {
                const firstInstanceValue = instanceArray[0][0]; // Extract value from [value, friendlyName] tuple
                masterSelector.value = firstInstanceValue;
                this.handleTableInstanceChange(deviceId, mibName, device, tableWidget, firstInstanceValue);
            }

            // Try to restore previously selected instance from state
            const restoredInstance = this.restoreInstanceSelection(deviceId, mibName, tableName, masterSelector);
            if (restoredInstance) {
                this.handleTableInstanceChange(deviceId, mibName, device, tableWidget, restoredInstance);
            }

            // Add change listener
            masterSelector.addEventListener('change', (e) => {
                this.handleTableInstanceChange(deviceId, mibName, device, tableWidget, e.target.value);
            });

        } catch (error) {
            console.error('Error querying instances, hiding section:', error);
            const tableWidgetSection = masterSelector.closest('.table-widget-section');
            if (tableWidgetSection) {
                tableWidgetSection.style.display = 'none';
                tableWidgetSection.classList.add('no-data-hidden');
            }
        }
    }

    handleTableInstanceChange(deviceId, mibName, device, tableWidget, selectedInstance) {
        const tableName = tableWidget.table_name;
        // Save instance selection to state
        if (selectedInstance) {
            this.saveInstanceSelection(deviceId, mibName, tableName, selectedInstance);
        }
        console.log(`Instance changed for ${tableName}: ${selectedInstance}`);

        if (!selectedInstance) {
            // Clear all fields
            for (const field of tableWidget.status_fields || []) {
                const statusId = `status-${deviceId}-${mibName}-${tableName}-${field.name}`;
                const statusElement = document.getElementById(statusId);
                if (statusElement) {
                    statusElement.textContent = 'Select an instance above';
                }
            }
            return;
        }

        const key = `${deviceId}-${mibName}-${tableName}`;
        let subscriptionData = this.activeSubscriptions.get(key);

        if (!subscriptionData) {
            subscriptionData = {
                metricQueries: new Set(),
                statusQueries: new Set(),
                objects: new Map()
            };
            this.activeSubscriptions.set(key, subscriptionData);
        }

        // Unsubscribe from old queries
        const allOldQueries = [...subscriptionData.statusQueries, ...subscriptionData.metricQueries];
        if (allOldQueries.length > 0 && this.ws && this.ws.readyState === WebSocket.OPEN) {
            this.ws.send(JSON.stringify({
                type: 'unsubscribe',
                queries: allOldQueries
            }));
        }

        subscriptionData.statusQueries.clear();
        subscriptionData.metricQueries.clear();
        subscriptionData.objects.clear();

        const instanceSelector = tableWidget.instance_selector;
        // Every row of a table column shares one metric name, so the row is
        // picked out by matching the index label the selector names.
        const matchers = this.instanceMatchers(instanceSelector, selectedInstance);

        // Subscribe to status fields
        for (const field of tableWidget.status_fields || []) {
            const statusId = `status-${deviceId}-${mibName}-${tableName}-${field.name}`;

            // field.metric is already the info metric name when the column's
            // value is a string or an enum, and the column's own metric
            // otherwise; the distinction is the server's to make.
            const metric = this.specMetric(field, `${mibName}.${tableName}.${field.name}`);
            if (!metric) {
                this.markStatusSpecError(statusId, MonitoringApp.MISSING_METRIC_DETAIL);
                continue;
            }

            const query = this.metricQuery(metric, device.name, matchers);

            subscriptionData.statusQueries.add(query);
            subscriptionData.objects.set(query, {
                ...field,
                statusId: statusId,
                enumValues: field.enum_values,
                isInfoMetric: field.is_info_metric
            });
        }

        // Subscribe to metric charts
        for (const chart of tableWidget.metric_charts || []) {
            const chartId = `chart-${deviceId}-${mibName}-${tableName}-${chart.name}`;

            // Reinitialize chart
            const existingChart = this.charts[chartId];
            if (existingChart) {
                existingChart.dispose();
            }
            this.initializeEmptyChart(chartId, chart.name);

            const metric = this.specMetric(chart, `${mibName}.${tableName}.${chart.name}`);
            if (!metric) {
                this.markChartSpecError(chartId, MonitoringApp.MISSING_METRIC_DETAIL);
                continue;
            }

            let query = this.metricQuery(metric, device.name, matchers);

            // Wrap counter metrics in rate() to show per-second values (bandwidth)
            if (chart.use_rate) {
                query = `rate(${query}[1m])`;
                console.log(`Using rate() for counter metric ${chart.name}: ${query}`);
            }

            subscriptionData.metricQueries.add(query);
            subscriptionData.objects.set(query, {
                ...chart,
                chartId: chartId,
                useRate: chart.use_rate
            });
        }

        // Send subscribe message
        const allQueries = [...subscriptionData.statusQueries, ...subscriptionData.metricQueries];
        if (this.ws && this.ws.readyState === WebSocket.OPEN) {
            console.log(`Subscribing to ${allQueries.length} queries for instance ${selectedInstance}`);
            this.ws.send(JSON.stringify({
                type: 'subscribe',
                queries: allQueries
            }));
        }
        
        // Force resize all charts after instance change
        setTimeout(() => {
            for (const chart of tableWidget.metric_charts || []) {
                const chartId = `chart-${deviceId}-${mibName}-${tableName}-${chart.name}`;
                const chartInstance = this.charts[chartId];
                if (chartInstance && chartInstance.resize) {
                    chartInstance.resize();
                }
            }
        }, 100);
    }

    // queryAvailableInstances lists the rows of a table column, one entry per
    // row of the series named by widget.metric. instanceSelector names the
    // labels: label_name is the column offered for selection, index_labels are
    // the table's whole index, and friendly_name_label is the human-readable
    // column to caption a row with, if the table has one.
    async queryAvailableInstances(deviceName, widget, instanceSelector) {
        const metric = this.specMetric(widget, `instances for ${widget && widget.name}`);
        if (!metric) return [];

        const selector = instanceSelector || {};
        const labelName = selector.label_name;
        if (!labelName) {
            console.error('Instance selector has no label_name; cannot list instances', selector);
            return [];
        }

        // Every row of the column carries the same metric name and differs
        // only in its index labels, so one name plus the device selects them
        // all -- no name regex, and no scan of every series name in the TSDB.
        const query = this.metricQuery(metric, deviceName);

        try {
            const response = await fetch('/api/metrics/instant', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                    query: query
                })
            });

            if (!response.ok) {
                console.error('Failed to query Prometheus for instances');
                return [];
            }

            const data = await response.json();
            const instances = [];
            const seenRows = new Set();

            // The whole index identifies a row exactly; label_name identifies
            // it only as far as that one column is unique within the table, so
            // rows are deduplicated on the full index and not on the offered
            // column alone.
            const indexLabels = (selector.index_labels && selector.index_labels.length > 0)
                ? selector.index_labels
                : [labelName];

            if (data.metrics && Array.isArray(data.metrics)) {
                for (const series of data.metrics) {
                    const labels = series.metric || {};

                    // The row index is in labels now, not in the metric name.
                    const instanceId = labels[labelName];
                    if (instanceId === undefined || instanceId === '') continue;

                    // U+001F (unit separator) joins the index values: no index
                    // label carries one, so two different rows cannot collide on
                    // the joined key.
                    const rowKey = indexLabels.map(label => labels[label] ?? '').join('\u001f');
                    if (seenRows.has(rowKey)) continue;
                    seenRows.add(rowKey);

                    const friendlyName = selector.friendly_name_label
                        ? labels[selector.friendly_name_label]
                        : null;

                    // Every index label of this row, for a caller that has to
                    // pin a row whose offered column is not unique.
                    const rowIndex = {};
                    for (const label of indexLabels) {
                        if (labels[label] !== undefined) rowIndex[label] = labels[label];
                    }

                    instances.push({
                        instance: instanceId,
                        label: friendlyName || instanceId,
                        index: instanceId,
                        indexLabels: rowIndex,
                        labels: labels
                    });
                }
            }

            return instances;
        } catch (error) {
            console.error('Error querying available instances:', error);
            return [];
        }
    }

    // instanceSelector is the table's selector from the dashboard spec: which
    // label the chosen value lives in is a property of the table, not
    // something to be guessed from the shape of the value, so BGP peers and
    // CPU cores select the same way interfaces do.
    handleInstanceChange(chartId, deviceId, mibName, deviceName, obj, instanceSelector, selectedInstance) {
        console.log(`Instance changed for ${chartId}: ${selectedInstance}`);

        // Store selection
        this.instanceSelections[chartId] = selectedInstance;

        // Clear chart and reinitialize
        const chart = this.charts[chartId];
        if (chart) {
            chart.dispose();
        }

        // Reinitialize chart
        this.initializeEmptyChart(chartId, obj.name);

        const metric = this.specMetric(obj, `${mibName}.${obj.name}`);
        if (!metric) {
            this.markChartSpecError(chartId, MonitoringApp.MISSING_METRIC_DETAIL);
            return;
        }

        // Unsubscribe from old query and subscribe to new one
        const key = `${deviceId}-${mibName}`;
        const subscriptionData = this.activeSubscriptions.get(key);

        if (subscriptionData) {
            const oldQuery = this.metricQuery(metric, deviceName);

            // Build new query based on selection
            let newQuery;
            if (selectedInstance === '__all__') {
                newQuery = oldQuery; // Keep broad query for all instances
            } else if (selectedInstance) {
                newQuery = this.metricQuery(metric, deviceName,
                    this.instanceMatchers(instanceSelector, selectedInstance));
            } else {
                // No selection, don't subscribe
                return;
            }

            // Remove old query from subscription
            if (subscriptionData.metricQueries.has(oldQuery)) {
                subscriptionData.metricQueries.delete(oldQuery);
                subscriptionData.objects.delete(oldQuery);

                if (this.ws && this.ws.readyState === WebSocket.OPEN) {
                    this.ws.send(JSON.stringify({
                        type: 'unsubscribe',
                        queries: [oldQuery]
                    }));
                }
            }

            // Add new query to subscription
            subscriptionData.metricQueries.add(newQuery);
            subscriptionData.objects.set(newQuery, obj);

            if (this.ws && this.ws.readyState === WebSocket.OPEN) {
                this.ws.send(JSON.stringify({
                    type: 'subscribe',
                    queries: [newQuery]
                }));
            }

            console.log(`Switched subscription from ${oldQuery} to ${newQuery}`);
        }
    }

    handleStatusInstanceChange(statusId, deviceId, mibName, deviceName, obj, instanceSelector, selectedInstance) {
        console.log(`Status instance changed for ${statusId}: ${selectedInstance}`);

        if (!selectedInstance) {
            // No selection, clear status
            const statusElement = document.getElementById(statusId);
            if (statusElement) {
                statusElement.textContent = 'Select an instance';
            }
            return;
        }

        const metric = this.specMetric(obj, `${mibName}.${obj.name}`);
        if (!metric) {
            this.markStatusSpecError(statusId, MonitoringApp.MISSING_METRIC_DETAIL);
            return;
        }

        // Subscribe to instance-specific query for this status field
        const key = `${deviceId}-${mibName}`;
        const subscriptionData = this.activeSubscriptions.get(key);

        if (subscriptionData) {
            const query = this.metricQuery(metric, deviceName,
                this.instanceMatchers(instanceSelector, selectedInstance));

            // Add query to status subscriptions
            subscriptionData.statusQueries.add(query);
            subscriptionData.objects.set(query, obj);

            // Send subscribe message
            if (this.ws && this.ws.readyState === WebSocket.OPEN) {
                this.ws.send(JSON.stringify({
                    type: 'subscribe',
                    queries: [query]
                }));
            }

            // The tile is now waiting on a different instance, so it goes back
            // to waiting and gets the same window every other widget gets.
            // Leaving the previous instance's value on screen would be worse
            // than saying nothing at all.
            const statusElement = document.getElementById(statusId);
            if (statusElement) {
                this.clearStatusNoData(statusElement);
                statusElement.textContent = 'Loading...';
            }
            setTimeout(() => this.checkStatusDataTimeout(statusId), FIRST_SAMPLE_TIMEOUT);

            console.log(`Subscribed to status field query: ${query}`);
        }
    }

    subscribeToDashboard(deviceId, mibName, device, statusObjects, metricObjects) {
        console.log(`Subscribing to dashboard: ${deviceId}-${mibName}`);
        console.log(`  Status objects: ${statusObjects.length}, Metric objects: ${metricObjects.length}`);

        const key = `${deviceId}-${mibName}`;
        const metricQueries = new Set();
        const statusQueries = new Set();
        const objects = new Map(); // Map query -> object info

        // Process status objects
        for (const obj of statusObjects) {
            console.log(`Processing status object: ${obj.name}, OID: ${obj.oid}, Type: ${obj.type}`);
            // The server names the metric: one name per MIB object, with any
            // row index in labels, so a single name covers every row.
            const metric = this.specMetric(obj, `${mibName}.${obj.name}`);
            if (!metric) {
                this.markStatusSpecError(
                    `status-${deviceId}-${mibName}-${obj.name}`,
                    MonitoringApp.MISSING_METRIC_DETAIL);
                continue;
            }

            const query = this.metricQuery(metric, device.name);
            console.log(`Generated status query: ${query}`);
            statusQueries.add(query);
            objects.set(query, obj);
        }

        // Process metric objects
        for (const obj of metricObjects) {
            // Initialize chart with empty data (waiting for WebSocket stream)
            const chartId = `chart-${deviceId}-${mibName}-${obj.name}`;
            this.initializeEmptyChart(chartId, obj.name);

            // The server names the metric: one name per MIB object, with any
            // row index in labels, so a single name covers every row.
            const metric = this.specMetric(obj, `${mibName}.${obj.name}`);
            if (!metric) {
                this.markChartSpecError(chartId, MonitoringApp.MISSING_METRIC_DETAIL);
                continue;
            }

            const query = this.metricQuery(metric, device.name);
            metricQueries.add(query);
            objects.set(query, obj);
        }

        // Send subscribe message to WebSocket for all queries
        const allQueries = [...statusQueries, ...metricQueries];
        if (this.ws && this.ws.readyState === WebSocket.OPEN) {
            console.log(`Sending WebSocket subscribe for ${allQueries.length} queries:`);
            allQueries.forEach((q, i) => console.log(`  [${i}] ${q}`));
            this.ws.send(JSON.stringify({
                type: 'subscribe',
                queries: allQueries
            }));
        } else {
            console.warn('WebSocket not ready, subscription will be sent on reconnect');
        }

        // Track subscriptions
        this.activeSubscriptions.set(key, {
            metricQueries,
            statusQueries,
            objects
        });
        console.log(`Active subscriptions now:`, Array.from(this.activeSubscriptions.keys()));
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
            const mibName = key.split('-')[1];
            this.unsubscribeFromDashboard(deviceId, mibName);
        });
    }

    initializeEmptyChart(chartId, title) {
        const chartElement = document.getElementById(chartId);
        if (!chartElement) {
            console.warn(`Chart element not found: ${chartId}`);
            return;
        }

        console.log(`Initializing empty chart: ${chartId}`);

        // The card starts collapsed, so the canvas has no height to measure
        // yet. ECharts still needs a size at init; the real one is applied by
        // revealChart() when the first sample arrives.
        this.resetChartCard(chartId);

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

        // Convert "Waiting for data…" to the terminal no-data state once the
        // shared first-sample window has elapsed.
        setTimeout(() => {
            this.checkChartDataTimeout(chartId);
        }, FIRST_SAMPLE_TIMEOUT);
    }

    checkChartDataTimeout(chartId) {
        const chart = this.charts[chartId];
        if (!chart || chart._hasData) return;

        // The card keeps its place in the grid: a MIB whose objects the device
        // does not populate should read as "nothing here", not disappear.
        // The instance stays live so a late first sample still draws.
        console.log(`Chart ${chartId} timed out waiting for data`);
        this.markChartNoData(chartId);
    }

    // The tile keeps its place, exactly as the chart cards do: an object the
    // device does not populate should read as "nothing here", not vanish and
    // leave the operator wondering which objects they were promised.
    checkStatusDataTimeout(statusId) {
        const statusElement = document.getElementById(statusId);
        if (!statusElement || statusElement.textContent !== 'Loading...') return;

        console.log(`Status ${statusId} timed out waiting for data`);
        this.markStatusNoData(statusId);
    }

    resubscribeActiveDashboards() {
        console.log('Re-subscribing to active dashboards after WebSocket reconnection');

        if (!this.ws || this.ws.readyState !== WebSocket.OPEN) {
            console.warn('WebSocket not ready for re-subscription');
            return;
        }

        // Re-subscribe to all active dashboards
        this.activeSubscriptions.forEach((subscriptionData, key) => {
            const { metricQueries, statusQueries } = subscriptionData;
            const allQueries = [...metricQueries, ...statusQueries];
            console.log(`Re-subscribing to ${key} with ${allQueries.length} queries`);
            this.ws.send(JSON.stringify({
                type: 'subscribe',
                queries: allQueries
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
                // Store selected device ID for restoration after devices load
                this.restoredDeviceId = state.selectedDeviceId;
                this.restoredMibTab = state.activeMibTab;
                this.restoredInstanceSelections = state.instanceSelections || {};
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
                selectedDeviceId: deviceSelect ? deviceSelect.value : null,
                activeMibTab: this.activeMibTab,
                instanceSelections: this.instanceSelections || {},
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

    saveInstanceSelection(deviceId, mibName, widgetId, instance) {
        const key = `${deviceId}-${mibName}-${widgetId}`;
        this.instanceSelections[key] = instance;
        this.saveState();
    }

    getInstanceSelection(deviceId, mibName, widgetId) {
        const key = `${deviceId}-${mibName}-${widgetId}`;
        return this.instanceSelections[key] || null;
    }

    restoreInstanceSelection(deviceId, mibName, widgetId, selector) {
        const savedInstance = this.getInstanceSelection(deviceId, mibName, widgetId);
        if (savedInstance && selector) {
            // Check if the option exists
            const option = selector.querySelector(`option[value="${savedInstance}"]`);
            if (option) {
                selector.value = savedInstance;
                return savedInstance;
            }
        }
        return null;
    }

    // ========================================
    // UI Helpers
    // ========================================

    // showNotification shows a transient message. holdMs is how long it stays;
    // the default suits a few words, and a message that explains something —
    // why a MIB will produce no metrics, say — needs longer than a reader takes
    // to notice it appeared.
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
