(function () {
  "use strict";
  var config = {{.ConfigJSON}};
  var host = "{{.HostUrl}}";
  var guestbookID = "{{.Guestbook.ID}}";
  var powEnabled = {{if .Guestbook.PowEnabled}}true{{else}}false{{end}};
  var key = JSON.stringify([host, guestbookID, config, powEnabled]);

  function initialize() {
    var form = document.getElementById("guestbooks___guestbook-form");
    var messagesContainer = document.getElementById("guestbooks___guestbook-messages-container");
    var existing = window.guestbooks___instance;
    if (existing && existing.form === form && existing.messagesContainer === messagesContainer &&
        form && form.isConnected && messagesContainer && messagesContainer.isConnected && existing.key === key) return;
    if (existing) existing.dispose();
    if (!form) {
      console.error("Guestbook initialization failed: the guestbook form is missing.");
      return;
    }
    var submitButtons = form.querySelectorAll("input[type='submit'], button[type='submit'], button:not([type])");
    if (!messagesContainer || !submitButtons.length || !form.elements.namedItem("name") || !form.elements.namedItem("text")) {
      var error = document.createElement("p");
      error.setAttribute("role", "alert");
      error.textContent = "The guestbook embed is incomplete. Restore the form fields, submit button, and messages container from the embed code.";
      form.appendChild(error);
      return;
    }

    var lifetime = new AbortController();
    var signal = lifetime.signal;
    var generatedNodes = [];
    var submissionInFlight = false;
    var powReady = !powEnabled;
    var previousSubmitStates = null;
    var resetPow = null;
    var stopPow = null;
    var observer = null;
    var feedbackRegion = form.querySelector("#guestbooks___feedback-container");
    var currentPage = 1;
    var isLoading = false;
    var hasMorePages = true;
    var reloadRequested = false;
    var failedReset = false;
    var loadFailed = false;

    function listen(element, event, callback) {
      element.addEventListener(event, callback, { signal: signal });
    }
    function track(element) {
      generatedNodes.push(element);
      return element;
    }
    function updateSubmitState() {
      if (submissionInFlight || !powReady) {
        if (!previousSubmitStates) {
          previousSubmitStates = Array.from(submitButtons, function (button) { return button.disabled; });
        }
        submitButtons.forEach(function (button) { button.disabled = true; });
      } else if (previousSubmitStates) {
        submitButtons.forEach(function (button, index) { button.disabled = previousSubmitStates[index]; });
        previousSubmitStates = null;
      }
    }
    function feedbackContainer(id, role) {
      var container = form.querySelector("#" + id);
      if (!container) {
        container = document.createElement("div");
        container.id = id;
        (feedbackRegion || form).appendChild(container);
      }
      container.setAttribute("role", role);
      if (role === "status") container.setAttribute("aria-live", "polite");
      container.style.whiteSpace = "pre-wrap";
      container.hidden = false;
      if (feedbackRegion) feedbackRegion.hidden = false;
      return container;
    }

    var validators = [];
    var validatedFields = [];
    function limitField(name, maximum, bytes, required) {
      var input = form.elements.namedItem(name);
      if (!input || typeof input.setCustomValidity !== "function") return;
      function validate() {
        var value = input.value.trim();
        var length = bytes ? new TextEncoder().encode(value).length : Array.from(value).length;
        input.setCustomValidity(required && !value ? "Please fill out this field." :
          length > maximum ? "Use at most " + maximum + (bytes ? " bytes." : " characters.") : "");
      }
      listen(input, "input", validate);
      validatedFields.push(input);
      validators.push(validate);
      validate();
    }
    limitField("name", config.maxNameCharacters, false, true);
    limitField("text", config.maxMessageCharacters, false, true);
    limitField("website", config.maxWebsiteBytes, true, false);
    if (config.collectEmail) limitField("email", config.maxEmailBytes, true, false);

    if (config.question.trim()) {
      var challengeContainer = form.querySelector("#guestbooks___challenge-answer-container") ||
        form.querySelector("#guestbooks___challenge\u2014answer\u2014container");
      if (!challengeContainer) {
        challengeContainer = track(document.createElement("div"));
        challengeContainer.id = "guestbooks___challenge-answer-container";
        submitButtons[0].parentNode.insertBefore(challengeContainer, submitButtons[0]);
      }
      var challengeField = track(document.createElement("div"));
      challengeField.className = "guestbooks___input-container";
      var label = document.createElement("label");
      label.htmlFor = "challengeQuestionAnswer";
      label.textContent = config.question;
      var answer = document.createElement("input");
      answer.type = "text";
      answer.id = "challengeQuestionAnswer";
      answer.name = "challengeQuestionAnswer";
      answer.placeholder = config.hint;
      answer.required = true;
      challengeField.append(label, document.createElement("br"), answer);
      challengeContainer.replaceChildren(challengeField);
    }

    var loadControls = track(document.createElement("div"));
    loadControls.id = "guestbooks___message-loading";
    var loadStatus = document.createElement("p");
    loadStatus.id = "guestbooks___message-load-status";
    loadStatus.setAttribute("role", "status");
    loadStatus.setAttribute("aria-live", "polite");
    var loadMore = document.createElement("button");
    loadMore.id = "guestbooks___load-more";
    loadMore.type = "button";
    loadMore.textContent = "Load more messages";
    loadControls.append(loadStatus, loadMore);
    messagesContainer.insertAdjacentElement("afterend", loadControls);
    listen(loadMore, "click", function () { loadMessages(loadFailed && failedReset); });

    function validMessage(message) {
      return message && typeof message.Name === "string" && typeof message.Text === "string" &&
        typeof message.CreatedAt === "string" && !Number.isNaN(Date.parse(message.CreatedAt)) &&
        (message.Website == null || typeof message.Website === "string");
    }
    function messageElement(message, reply) {
      var element = document.createElement("div");
      element.className = "guestbook-message" + (reply ? " guestbook-message-reply" : "");
      var header = document.createElement("p");
      var name = document.createElement("b");
      var website = null;
      if (message.Website && !reply) {
        try {
          var candidate = new URL(message.Website);
          if (candidate.protocol === "https:" || candidate.protocol === "http:") website = candidate;
        } catch (_) {
          // Old messages may have a non-URL website; show their name without a link.
        }
      }
      if (website) {
        var link = document.createElement("a");
        link.href = website.href;
        link.textContent = message.Name;
        link.target = "_blank";
        link.rel = "ugc nofollow noopener noreferrer";
        name.appendChild(link);
      } else {
        name.textContent = message.Name;
      }
      var date = document.createElement("small");
      date.textContent = " - " + new Date(message.CreatedAt).toLocaleDateString();
      var text = document.createElement("blockquote");
      text.textContent = message.Text;
      header.append(name, date);
      element.append(header, text);
      return element;
    }
    async function loadMessages(reset) {
      if (signal.aborted) return;
      if (isLoading) {
        if (reset) reloadRequested = true;
        return;
      }
      if (!hasMorePages && !reset && !loadFailed) return;
      var page = reset ? 1 : currentPage;
      isLoading = true;
      loadMore.disabled = true;
      loadStatus.textContent = "Loading messages...";
      if (observer) observer.disconnect();
      try {
        var response = await fetch(host + "/api/v2/get-guestbook-messages/" + guestbookID + "?page=" + page + "&limit=20", { signal: signal });
        if (!response.ok || response.redirected) throw new Error("Message request failed");
        var data = await response.json();
        if (!data || !Object.prototype.hasOwnProperty.call(data, "messages") ||
            !(data.messages === null || Array.isArray(data.messages)) ||
            !data.pagination || typeof data.pagination.hasNext !== "boolean") {
          throw new Error("Invalid message response");
        }
        var messages = data.messages || [];
        if (!messages.every(function (message) {
          return validMessage(message) && (message.Replies == null ||
            Array.isArray(message.Replies) && message.Replies.every(validMessage));
        })) throw new Error("Invalid message data");
        var fragment = document.createDocumentFragment();
        messages.forEach(function (message) {
          if (message.ParentMessageID != null) return;
          fragment.appendChild(messageElement(message, false));
          (message.Replies || []).forEach(function (reply) { fragment.appendChild(messageElement(reply, true)); });
        });
        if (reset) messagesContainer.replaceChildren();
        if (messages.length === 0 && page === 1) {
          var empty = document.createElement("p");
          empty.textContent = "There are no messages on this guestbook.";
          fragment.appendChild(empty);
        }
        messagesContainer.appendChild(fragment);
        hasMorePages = data.pagination.hasNext;
        currentPage = page + 1;
        loadFailed = false;
        loadStatus.textContent = "";
        loadMore.textContent = "Load more messages";
        loadMore.hidden = !hasMorePages;
      } catch (error) {
        if (signal.aborted) return;
        loadFailed = true;
        failedReset = reset;
        loadStatus.textContent = "Could not load messages. Your displayed messages have been kept. Try again.";
        loadMore.textContent = "Retry loading messages";
        loadMore.hidden = false;
        console.error("Guestbook message loading failed:", error);
      } finally {
        isLoading = false;
        loadMore.disabled = false;
        if (!signal.aborted && reloadRequested) {
          reloadRequested = false;
          loadMessages(true);
        } else if (observer && hasMorePages && !loadFailed && !signal.aborted) {
          observer.observe(loadMore);
        }
      }
    }
    if (typeof IntersectionObserver === "function") {
      observer = new IntersectionObserver(function (entries) {
        if (entries.some(function (entry) { return entry.isIntersecting; }) && !isLoading && !loadFailed) loadMessages(false);
      }, { rootMargin: "200px", threshold: 0.1 });
    }

    listen(form, "submit", async function (event) {
      event.preventDefault();
      if (submissionInFlight) return;
      validators.forEach(function (validate) { validate(); });
      if (!form.reportValidity()) return;
      if (feedbackRegion) feedbackRegion.hidden = true;
      var errorContainer = form.querySelector("#guestbooks___error-message");
      var successContainer = form.querySelector("#guestbooks___success-message");
      if (errorContainer) errorContainer.textContent = "";
      if (successContainer) {
        successContainer.textContent = "";
        successContainer.hidden = true;
      }
      if (!powReady) {
        feedbackContainer("guestbooks___error-message", "alert").textContent = "Please complete the verification before submitting.";
        return;
      }
      submissionInFlight = true;
      updateSubmitState();
      try {
        var response = await fetch(form.action, {
          method: "POST", headers: { Accept: "application/json" }, body: new FormData(form), signal: signal
        });
        if (!response.ok) {
          var errorText = await response.text();
          feedbackContainer("guestbooks___error-message", "alert").textContent =
            response.status === 401 && config.challengeFailedMessage ? config.challengeFailedMessage :
              errorText || "Your message could not be submitted. Please try again.";
          return;
        }
        if (response.redirected) throw new Error("Unexpected submission redirect");
        var result = await response.json();
        if (!result || result.success !== true || typeof result.message !== "string" || typeof result.redirectUrl !== "string") {
          throw new Error("Invalid submission response");
        }
        if (result.redirectUrl) {
          var destination = new URL(result.redirectUrl);
          if ((destination.protocol !== "http:" && destination.protocol !== "https:") || destination.username || destination.password) {
            throw new Error("Invalid redirect response");
          }
          window.location.assign(destination.href);
          return;
        }
        form.reset();
        // Reset custom validity too, allowing native required-field checks to run afresh.
        Array.from(form.elements).forEach(function (field) {
          if (typeof field.setCustomValidity === "function") field.setCustomValidity("");
        });
        if (result.message) feedbackContainer("guestbooks___success-message", "status").textContent = result.message;
        loadMessages(true);
      } catch (error) {
        if (signal.aborted) return;
        console.error("Submission could not be confirmed:", error);
        feedbackContainer("guestbooks___error-message", "alert").textContent = "Could not confirm whether your message was received. Check the guestbook before trying again.";
      } finally {
        if (resetPow && !signal.aborted) resetPow();
        submissionInFlight = false;
        updateSubmitState();
      }
    });

    if (powEnabled) {
      var powWorker = null;
      var workerURL = null;
      var powRequest = null;
      var powTimer = null;
      var job = 0;
      var powContainer = form.querySelector("#guestbooks___pow-status");
      if (!powContainer) {
        powContainer = track(document.createElement("div"));
        form.appendChild(powContainer);
      }
      var originalPowID = powContainer.id;
      powContainer.id = "guestbooks___pow-container";
      powContainer.className = "guestbooks___pow-container";
      var powLabel = track(document.createElement("label"));
      powLabel.className = "guestbooks___pow-checkbox-label";
      var powCheckbox = document.createElement("input");
      powCheckbox.type = "checkbox";
      powCheckbox.id = "guestbooks___pow-checkbox";
      var powText = document.createElement("span");
      powText.id = "guestbooks___pow-status";
      powText.setAttribute("role", "status");
      powText.setAttribute("aria-live", "polite");
      powLabel.append(powCheckbox, powText);
      powContainer.replaceChildren(powLabel);
      var hiddenChallenge = track(document.createElement("input"));
      hiddenChallenge.type = "hidden";
      hiddenChallenge.name = "powChallenge";
      var hiddenNonce = track(document.createElement("input"));
      hiddenNonce.type = "hidden";
      hiddenNonce.name = "powNonce";
      form.append(hiddenChallenge, hiddenNonce);

      function unsupportedReason() {
        if (!window.isSecureContext) return "Verification requires HTTPS. Open this guestbook on an HTTPS site, or ask the site owner to enable HTTPS.";
        if (!window.crypto || !window.crypto.subtle || typeof Worker !== "function" || !URL.createObjectURL) {
          return "Verification requires Web Crypto and Web Workers. Use a current browser with these features enabled, then reload.";
        }
        return "";
      }
      function clearWork() {
        if (powRequest) powRequest.abort();
        if (powWorker) powWorker.terminate();
        if (workerURL) URL.revokeObjectURL(workerURL);
        clearTimeout(powTimer);
        powRequest = null;
        powWorker = null;
        workerURL = null;
      }
      function failPow(message) {
        clearWork();
        powReady = false;
        hiddenChallenge.value = "";
        hiddenNonce.value = "";
        powCheckbox.checked = false;
        powCheckbox.disabled = !!unsupportedReason();
        powText.textContent = message;
        powText.className = "guestbooks___pow-label-text--error";
        updateSubmitState();
      }
      var workerCode = `
        self.onmessage = async function(event) {
          try {
            var challenge = event.data.challenge;
            var difficulty = event.data.difficulty;
            for (var nonce = 0; ; nonce++) {
              var value = nonce.toString(16);
              var hash = new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(challenge + value)));
              var valid = true;
              for (var bit = 0; bit < difficulty; bit++) {
                if (hash[Math.floor(bit / 8)] & (128 >> (bit % 8))) { valid = false; break; }
              }
              if (valid) { self.postMessage({found:true, nonce:value}); return; }
            }
          } catch (error) { self.postMessage({error:true}); }
        };
      `;
      listen(powCheckbox, "change", async function () {
        if (!powCheckbox.checked) return;
        var unsupported = unsupportedReason();
        if (unsupported) { failPow(unsupported); return; }
        clearWork();
        var activeJob = ++job;
        powReady = false;
        updateSubmitState();
        powCheckbox.disabled = true;
        powText.textContent = "Verifying\u2026";
        powText.className = "guestbooks___pow-label-text--loading";
        powRequest = new AbortController();
        powTimer = setTimeout(function () {
          job++;
          failPow("Verification timed out. Check the box to try again.");
        }, 60000);
        try {
          var response = await fetch(host + "/api/pow-challenge/" + guestbookID, { signal: powRequest.signal });
          if (!response.ok || response.redirected) throw new Error("Challenge unavailable");
          var data = await response.json();
          if (typeof data.challenge !== "string" || !data.challenge ||
              !Number.isInteger(data.difficulty) || data.difficulty < 1 || data.difficulty > 256) {
            throw new Error("Invalid challenge");
          }
          if (signal.aborted || activeJob !== job) return;
          workerURL = URL.createObjectURL(new Blob([workerCode], { type: "application/javascript" }));
          powWorker = new Worker(workerURL);
          powWorker.onerror = powWorker.onmessageerror = function (event) {
            if (event.preventDefault) event.preventDefault();
            if (activeJob === job) failPow("Verification worker failed. Check the box to retry, or use a current browser.");
          };
          powWorker.onmessage = function (event) {
            if (signal.aborted || activeJob !== job) return;
            if (event.data && event.data.found === true && typeof event.data.nonce === "string" && /^[0-9a-f]+$/i.test(event.data.nonce)) {
              hiddenChallenge.value = data.challenge;
              hiddenNonce.value = event.data.nonce;
              powReady = true;
              clearWork();
              powText.textContent = "Verified \u2713";
              powText.className = "guestbooks___pow-label-text--verified";
              updateSubmitState();
            } else if (!event.data || event.data.error) {
              failPow("Verification worker failed. Check the box to try again.");
            }
          };
          powWorker.postMessage({ challenge: data.challenge, difficulty: data.difficulty });
        } catch (error) {
          if (!signal.aborted && activeJob === job) failPow("Verification failed. Check the box to try again.");
        }
      });
      resetPow = function () {
        job++;
        clearWork();
        powReady = false;
        hiddenChallenge.value = "";
        hiddenNonce.value = "";
        powCheckbox.checked = false;
        var unsupported = unsupportedReason();
        powCheckbox.disabled = !!unsupported;
        powText.textContent = unsupported || "I\u2019m not a robot";
        powText.className = unsupported ? "guestbooks___pow-label-text--error" : "";
        updateSubmitState();
      };
      stopPow = function () {
        job++;
        clearWork();
        powContainer.id = originalPowID;
      };
      resetPow();
    }

    var removalObserver = new MutationObserver(function () {
      if (!form.isConnected || !messagesContainer.isConnected) dispose();
    });
    function dispose() {
      lifetime.abort();
      if (observer) observer.disconnect();
      removalObserver.disconnect();
      if (stopPow) stopPow();
      validatedFields.forEach(function (field) { field.setCustomValidity(""); });
      generatedNodes.forEach(function (node) { node.remove(); });
      powReady = true;
      submissionInFlight = false;
      updateSubmitState();
      if (window.guestbooks___instance && window.guestbooks___instance.form === form) delete window.guestbooks___instance;
    }
    window.guestbooks___instance = { form: form, messagesContainer: messagesContainer, key: key, dispose: dispose };
    removalObserver.observe(document.body, { childList: true, subtree: true });
    listen(window, "pagehide", function (event) { if (!event.persisted) dispose(); });
    updateSubmitState();
    loadMessages(true);
  }

  if (document.readyState === "loading") {
    if (window.guestbooks___pendingReady) document.removeEventListener("DOMContentLoaded", window.guestbooks___pendingReady);
    window.guestbooks___pendingReady = function () {
      delete window.guestbooks___pendingReady;
      initialize();
    };
    document.addEventListener("DOMContentLoaded", window.guestbooks___pendingReady, { once: true });
  } else {
    initialize();
  }
})();
