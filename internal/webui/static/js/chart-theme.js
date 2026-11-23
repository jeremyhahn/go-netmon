/*
 * ECharts theme for go-netmon.
 *
 * The theme is built from the CSS design tokens rather than a second hard-coded
 * palette, so charts follow the app's light/dark theme and there is exactly one
 * place a colour is defined. The categorical series colours are the validated
 * eight-slot palette and are assigned in fixed order — never cycled or
 * generated.
 *
 * This file loads before app.js and wraps echarts.init, so existing call sites
 * keep working unchanged while getting the themed defaults.
 */
(function () {
    'use strict';

    if (typeof echarts === 'undefined') return;

    var THEME = 'netmon';
    var instances = [];

    function token(name, fallback) {
        var value = getComputedStyle(document.documentElement).getPropertyValue(name);
        value = (value || '').trim();
        return value || fallback;
    }

    // The eight categorical slots, in the order the palette validates in.
    function seriesPalette() {
        var colors = [];
        for (var i = 1; i <= 8; i++) {
            colors.push(token('--series-' + i, '#3987e5'));
        }
        return colors;
    }

    function seriesColor(index) {
        return token('--series-' + ((index || 0) + 1), '#3987e5');
    }

    // The series tokens are hex; an area fill is the same colour at a lower
    // alpha rather than a second token, so the fill can never drift from
    // the line it belongs to.
    function withAlpha(color, alpha) {
        var hex = (color || '').trim();
        if (hex.charAt(0) !== '#') return hex;
        if (hex.length === 4) {
            hex = '#' + hex[1] + hex[1] + hex[2] + hex[2] + hex[3] + hex[3];
        }
        var n = parseInt(hex.slice(1), 16);
        return 'rgba(' + ((n >> 16) & 255) + ',' + ((n >> 8) & 255) + ',' + (n & 255) + ',' + alpha + ')';
    }

    function areaFill(index) {
        var color = seriesColor(index);
        return {
            color: {
                type: 'linear', x: 0, y: 0, x2: 0, y2: 1,
                colorStops: [
                    { offset: 0, color: withAlpha(color, 0.24) },
                    { offset: 1, color: withAlpha(color, 0.01) }
                ]
            }
        };
    }

    // The colour half of an axis, with none of the structure. A live theme
    // change merges this into charts that already exist, so it must not carry
    // show flags, formatters or intervals — those belong to the chart, not to
    // the palette, and re-asserting them would undo per-chart decisions such as
    // a suppressed vertical grid.
    function axisChrome() {
        var muted = token('--text-muted', '#6d7684');
        return {
            axisLine: { lineStyle: { color: token('--axis', 'rgba(255,255,255,0.14)') } },
            axisLabel: { color: muted },
            splitLine: { lineStyle: { color: token('--grid', 'rgba(255,255,255,0.06)') } },
            nameTextStyle: { color: muted }
        };
    }

    function buildTheme() {
        var text = token('--text', '#e9ecf1');
        var secondary = token('--text-secondary', '#a4adbb');
        var muted = token('--text-muted', '#6d7684');
        var grid = token('--grid', 'rgba(255,255,255,0.06)');
        var axis = token('--axis', 'rgba(255,255,255,0.14)');
        var surface = token('--surface', '#12151a');
        var raised = token('--surface-raised', '#171b21');
        var border = token('--border-strong', 'rgba(255,255,255,0.16)');

        // Axes are chrome, not data: hairline grid, no axis line shouting, and
        // ticks suppressed because the labels already mark the positions.
        var axisCommon = {
            axisLine: { show: true, lineStyle: { color: axis, width: 1 } },
            axisTick: { show: false },
            axisLabel: { color: muted, fontSize: 11, margin: 10, hideOverlap: true },
            splitLine: { show: true, lineStyle: { color: grid, width: 1 } },
            splitArea: { show: false },
            nameTextStyle: { color: muted, fontSize: 11, padding: [0, 0, 0, 4] }
        };

        return {
            color: seriesPalette(),
            backgroundColor: 'transparent',

            textStyle: {
                fontFamily: 'system-ui, -apple-system, "Segoe UI", Roboto, sans-serif',
                color: secondary,
                fontSize: 12
            },

            title: {
                textStyle: { color: text, fontSize: 13, fontWeight: 600 },
                subtextStyle: { color: muted, fontSize: 11 }
            },

            // Crosshair + tooltip is the default interaction for a time series.
            tooltip: {
                backgroundColor: raised,
                borderColor: border,
                borderWidth: 1,
                padding: [8, 10],
                textStyle: { color: text, fontSize: 12 },
                extraCssText: 'border-radius:9px;box-shadow:0 8px 28px rgba(0,0,0,0.28);',
                axisPointer: {
                    lineStyle: { color: axis, width: 1 },
                    crossStyle: { color: axis, width: 1 },
                    label: { backgroundColor: raised, color: text, borderColor: border, borderWidth: 1 }
                }
            },

            legend: {
                textStyle: { color: secondary, fontSize: 11 },
                inactiveColor: muted,
                itemWidth: 10,
                itemHeight: 10,
                itemGap: 14,
                icon: 'roundRect'
            },

            grid: { left: 8, right: 16, top: 28, bottom: 8, containLabel: true },

            categoryAxis: axisCommon,
            valueAxis: axisCommon,
            timeAxis: axisCommon,
            logAxis: axisCommon,

            // Thin marks, generous hit targets, no symbol clutter on dense lines.
            line: {
                smooth: true,
                symbol: 'circle',
                symbolSize: 8,
                showSymbol: false,
                lineStyle: { width: 2 },
                emphasis: { focus: 'series', lineStyle: { width: 2.5 } }
            },

            bar: {
                // 4px rounded data-ends, anchored to the baseline.
                itemStyle: { borderRadius: [4, 4, 0, 0], borderColor: surface, borderWidth: 2 },
                barMaxWidth: 28,
                emphasis: { focus: 'series' }
            },

            pie: {
                itemStyle: { borderColor: surface, borderWidth: 2 },
                label: { color: secondary, fontSize: 11 }
            },

            gauge: {
                axisLine: { lineStyle: { color: [[1, grid]], width: 10 } },
                axisLabel: { color: muted, fontSize: 10 },
                axisTick: { show: false },
                splitLine: { show: false },
                title: { color: muted, fontSize: 11 },
                detail: { color: text, fontSize: 20, fontWeight: 600 }
            },

            dataZoom: {
                borderColor: border,
                textStyle: { color: muted },
                handleStyle: { color: token('--accent', '#3987e5') },
                fillerColor: token('--accent-soft', 'rgba(57,135,229,0.14)')
            }
        };
    }

    // ----------------------------------------------------------------------
    // Time axis
    //
    // A fixed hour:minute label is wrong at both ends of the range the UI
    // offers: over a two minute window every tick reads the same, and over
    // days it loses the date. The label granularity is therefore chosen from
    // the span the series actually covers.
    // ----------------------------------------------------------------------

    var MINUTE = 60 * 1000;
    var HOUR = 60 * MINUTE;
    var DAY = 24 * HOUR;

    // Bands are ordered coarse-to-fine so a caller can compare them cheaply
    // and only rebuild the formatter when the band itself changes.
    var BAND_DATE = 0;      // three days or more: calendar date only
    var BAND_DATETIME = 1;  // one to three days: date over time
    var BAND_MINUTE = 2;    // three minutes to a day: hour:minute
    var BAND_SECOND = 3;    // under three minutes: hour:minute:second

    function timeBand(spanMs) {
        var span = (typeof spanMs === 'number' && isFinite(spanMs) && spanMs > 0) ? spanMs : 0;
        if (span >= 3 * DAY) return BAND_DATE;
        if (span >= DAY) return BAND_DATETIME;
        if (span >= 3 * MINUTE) return BAND_MINUTE;
        return BAND_SECOND;
    }

    function pad(n) {
        return n < 10 ? '0' + n : String(n);
    }

    // 24-hour clock: it is the convention in operations tooling, and it keeps
    // the tick narrow enough that five labels never collide.
    function clock(date, withSeconds) {
        var text = pad(date.getHours()) + ':' + pad(date.getMinutes());
        return withSeconds ? text + ':' + pad(date.getSeconds()) : text;
    }

    function calendar(date) {
        return date.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
    }

    function renderTime(band, value) {
        var date = new Date(value);
        if (band === BAND_DATE) return calendar(date);
        if (band === BAND_DATETIME) return calendar(date) + '\n' + clock(date, false);
        return clock(date, band === BAND_SECOND);
    }

    // The smallest step the labels of a band can tell apart. Without it ECharts
    // is free to place ticks half a second apart and every one of them prints
    // the same second.
    function timeAxisMinInterval(spanMs) {
        switch (timeBand(spanMs)) {
            case BAND_DATE: return DAY;
            case BAND_DATETIME: return MINUTE;
            case BAND_MINUTE: return MINUTE;
            default: return 1000;
        }
    }

    function timeAxisFormatter(spanMs) {
        var band = timeBand(spanMs);
        var lastText = null;
        var lastValue = -Infinity;

        // minInterval makes a repeat very unlikely; blanking makes it
        // impossible, including on the extra tick ECharts puts on an extent
        // boundary. Labels arrive in ascending order, one pass per render.
        return function (value, index) {
            var text = renderTime(band, value);
            if (index === 0 || value < lastValue) {
                lastValue = value;
                lastText = text;
                return text;
            }
            lastValue = value;
            if (text === lastText) return '';
            lastText = text;
            return text;
        };
    }

    // Tooltips always carry the full instant; only the axis is abbreviated.
    function timestampLabel(value, spanMs) {
        var date = new Date(value);
        var band = timeBand(spanMs);
        var time = date.toLocaleTimeString();
        return band <= BAND_DATETIME ? calendar(date) + ' ' + time : time;
    }

    // ----------------------------------------------------------------------
    // Value axis
    //
    // ECharts formats value ticks as floats, so an integer series with a range
    // of one renders "1 1 1 0 0 0". Integer series therefore get an integer
    // tick step, and fractional series get as many decimals as it takes for
    // neighbouring ticks to differ.
    // ----------------------------------------------------------------------

    function decimalsFor(value) {
        var abs = Math.abs(value);
        if (abs >= 100) return 1;
        if (abs >= 1) return 2;
        if (abs >= 0.01) return 3;
        return 4;
    }

    function formatTick(value) {
        if (Number.isInteger(value)) return String(value);
        var abs = Math.abs(value);
        if (abs > 0 && abs < 0.01) return String(Number(value.toPrecision(2)));
        return String(Number(value.toFixed(decimalsFor(value))));
    }

    // Public surface. app.js builds its options through this so no call site
    // has to know a colour, a date format or an axis rule.
    window.netmonCharts = {
        seriesColor: seriesColor,

        // Marks the chart so a theme change can re-derive the gradient, and
        // returns the areaStyle for its first series.
        areaStyle: function (chart, index) {
            if (chart) chart._netmonArea = index || 0;
            return areaFill(index || 0);
        },

        timeBand: timeBand,
        timeAxisFormatter: timeAxisFormatter,
        timeAxisMinInterval: timeAxisMinInterval,
        timestampLabel: timestampLabel,
        formatTick: formatTick
    };

    echarts.registerTheme(THEME, buildTheme());

    // Wrap init so every existing echarts.init(el, 'dark') call picks up the
    // themed defaults without touching the call sites.
    var originalInit = echarts.init;
    echarts.init = function (dom, theme, opts) {
        var chart = originalInit.call(echarts, dom, THEME, opts);
        instances.push(chart);
        return chart;
    };

    // A live instance cannot swap themes in place, so on a theme change push the
    // new chrome colours onto every chart. Colours that came from the palette
    // are re-applied wholesale; the data is untouched.
    window.addEventListener('netmon:themechange', function () {
        var next = buildTheme();
        echarts.registerTheme(THEME, next);

        instances = instances.filter(function (chart) {
            return chart && !chart.isDisposed();
        });

        var chrome = axisChrome();

        instances.forEach(function (chart) {
            try {
                var patch = {
                    color: next.color,
                    textStyle: next.textStyle,
                    tooltip: next.tooltip,
                    legend: next.legend,
                    xAxis: chrome,
                    yAxis: chrome
                };

                // An area fill is an explicit gradient, so unlike the line
                // colour it does not follow the palette on its own.
                if (chart._netmonArea !== undefined) {
                    patch.series = [{ areaStyle: areaFill(chart._netmonArea) }];
                }

                chart.setOption(patch);
            } catch (e) {
                // A chart mid-render is not worth failing the theme switch for.
            }
        });
    });

    // Charts do not reflow on their own when the layout changes.
    var resizeTimer = null;
    window.addEventListener('resize', function () {
        clearTimeout(resizeTimer);
        resizeTimer = setTimeout(function () {
            instances.forEach(function (chart) {
                if (chart && !chart.isDisposed()) {
                    try { chart.resize(); } catch (e) { /* detached */ }
                }
            });
        }, 120);
    });
})();
