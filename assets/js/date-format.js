var guestbooksDateFormat = (function () {
  "use strict";

  class PatternError extends Error {}

  function pad(value, length) {
    return String(value).padStart(length, "0");
  }

  // Use local calendar components in UTC arithmetic to avoid DST-length days.
  function calendarDate(year, month, day) {
    var date = new Date(0);
    date.setUTCFullYear(year, month, day);
    return date;
  }

  function dayOfYear(date) {
    return (calendarDate(date.getFullYear(), date.getMonth(), date.getDate()) -
      calendarDate(date.getFullYear(), 0, 1)) / 86400000 + 1;
  }

  function isoWeek(date) {
    var thursday = calendarDate(date.getFullYear(), date.getMonth(), date.getDate());
    thursday.setUTCDate(thursday.getUTCDate() + 4 - (thursday.getUTCDay() || 7));
    var year = thursday.getUTCFullYear();
    return {
      year: year,
      week: Math.ceil(((thursday - calendarDate(year, 0, 1)) / 86400000 + 1) / 7)
    };
  }

  var nameFormatters = {};
  function dateName(date, field, width) {
    var key = field + width;
    if (!nameFormatters[key]) {
      var options = { calendar: "gregory" };
      options[field] = width;
      nameFormatters[key] = new Intl.DateTimeFormat(undefined, options);
    }
    return nameFormatters[key].format(date);
  }

  var tokens = {
    Y: function (date) { return pad(date.getFullYear(), 4); },
    y: function (date) { return pad(date.getFullYear() % 100, 2); },
    m: function (date) { return pad(date.getMonth() + 1, 2); },
    d: function (date) { return pad(date.getDate(), 2); },
    e: function (date) { return String(date.getDate()).padStart(2, " "); },
    b: function (date) { return dateName(date, "month", "short"); },
    B: function (date) { return dateName(date, "month", "long"); },
    a: function (date) { return dateName(date, "weekday", "short"); },
    A: function (date) { return dateName(date, "weekday", "long"); },
    j: function (date) { return pad(dayOfYear(date), 3); },
    G: function (date) { return pad(isoWeek(date).year, 4); },
    g: function (date) { return pad(isoWeek(date).year % 100, 2); },
    V: function (date) { return pad(isoWeek(date).week, 2); },
    u: function (date) { return String(date.getDay() || 7); },
    w: function (date) { return String(date.getDay()); },
    "%": function () { return "%"; }
  };

  function compile(pattern) {
    if (typeof pattern !== "string" || /[\uD800-\uDFFF]/u.test(pattern)) {
      throw new PatternError("Use valid Unicode characters in the date format.");
    }
    var characters = Array.from(pattern);
    if (characters.length > 128) {
      throw new PatternError("Use at most 128 characters in the date format.");
    }
    if (/[\u0000-\u001F\u007F-\u009F\u2028\u2029]/.test(pattern)) {
      throw new PatternError("Date formats must be a single line without control characters.");
    }
    var parts = [];
    var hasDateToken = false;
    for (var i = 0; i < characters.length; i++) {
      if (characters[i] !== "%") {
        parts.push(characters[i]);
        continue;
      }
      i++;
      if (i === characters.length) {
        throw new PatternError("A trailing % needs a token; use %% for a literal percent sign.");
      }
      var token = characters[i];
      if (!Object.prototype.hasOwnProperty.call(tokens, token)) {
        throw new PatternError("Unsupported date token %" + token + ". Use %% for a literal percent sign.");
      }
      parts.push(tokens[token]);
      hasDateToken = hasDateToken || token !== "%";
    }
    if (!hasDateToken) {
      throw new PatternError("Include at least one date token, such as %Y.");
    }
    return function (date) {
      return parts.map(function (part) {
        return typeof part === "string" ? part : part(date);
      }).join("");
    };
  }

  function createFormatter(mode, pattern) {
    if (mode === "") return function (date) { return date.toLocaleDateString(); };
    if (mode === "custom") return compile(pattern);
    throw new PatternError("Unknown date display format.");
  }

  return { createFormatter: createFormatter, PatternError: PatternError };
})();
