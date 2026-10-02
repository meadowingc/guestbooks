(function () {
  var form = document.getElementById("guestbooks___guestbook-form");
  var messagesContainer = document.getElementById(
    "guestbooks___guestbook-messages-container"
  );
  var config = {{.ConfigJSON}};
  var submitButtons = form.querySelectorAll("input[type='submit'], button[type='submit'], button:not([type])");
  var submissionInFlight = false;
  var powReady = {{if .Guestbook.PowEnabled}}false{{else}}true{{end}};
  var resetPow = null;
  var reloadRequested = false;

  function updateSubmitState() {
    submitButtons.forEach(function (button) {
      button.disabled = submissionInFlight || !powReady;
    });
  }

  function feedbackContainer(id, role) {
    var container = form.querySelector("#" + id);
    if (!container) {
      container = document.createElement("div");
      container.id = id;
      form.appendChild(container);
    }
    container.setAttribute("role", role);
    if (role === "status") container.setAttribute("aria-live", "polite");
    container.style.whiteSpace = "pre-wrap";
    return container;
  }

  var emailInput = form.querySelector("input[name='email']");
  if (config.collectEmail && emailInput) {
    function validateEmailLength() {
      emailInput.setCustomValidity(new TextEncoder().encode(emailInput.value.trim()).length > config.maxEmailBytes
        ? "Email address must contain at most " + config.maxEmailBytes + " bytes." : "");
    }
    emailInput.addEventListener("input", validateEmailLength);
    validateEmailLength();
  }
  updateSubmitState();

  // paging state
  const pageSize = 20;
  var currentPage = 1;
  var isLoading = false;
  var hasMorePages = true;

  function guestbooks___formatDate(value) {
    return new Date(value).toLocaleDateString();
  }

  form.addEventListener("submit", async function (event) {
    event.preventDefault();
    if (submissionInFlight) return;
    var errorContainer = feedbackContainer("guestbooks___error-message", "alert");
    var successContainer = feedbackContainer("guestbooks___success-message", "status");
    errorContainer.textContent = "";
    successContainer.textContent = "";
    if (!powReady) {
      errorContainer.textContent = "Please complete the verification before submitting.";
      return;
    }
    submissionInFlight = true;
    updateSubmitState();
    try {
      const response = await fetch(form.action, {
        method: "POST",
        headers: { Accept: "application/json" },
        body: new FormData(form),
      });
      if (!response.ok) {
        const error = await response.text();
        console.error("Submission rejected:", response.status);
        errorContainer.textContent = response.status === 401 && config.challengeFailedMessage
          ? config.challengeFailedMessage : error || "Your message could not be submitted. Please try again.";
        return;
      }
      const result = await response.json();
      if (!result || result.success !== true || typeof result.message !== "string" || typeof result.redirectUrl !== "string") {
        throw new Error("Invalid submission response");
      }
      if (result.redirectUrl) {
        const destination = new URL(result.redirectUrl);
        if (destination.protocol !== "http:" && destination.protocol !== "https:") {
          throw new Error("Invalid redirect response");
        }
        window.location.assign(destination.href);
        return;
      }
      form.reset();
      successContainer.textContent = result.message;
      guestbooks___loadMessages(true);
    } catch (error) {
      console.error("Submission could not be confirmed:", error);
      errorContainer.textContent = "Could not confirm whether your message was received. Check the guestbook before trying again.";
    } finally {
      if (resetPow) resetPow();
      submissionInFlight = false;
      updateSubmitState();
    }
  });

  function guestbooks___populateQuestionChallenge() {
    const challengeQuestion = "{{.Guestbook.ChallengeQuestion}}";
    const challengeHint = "{{.Guestbook.ChallengeHint}}";

    if (challengeQuestion.trim().length === 0) {
      return;
    }

    let challengeContainer = document.querySelector("#guestbooks___challenge-answer-container") || document.querySelector("#guestbooks___challenge—answer—container")

    // Add challenge question to the form if 
    if (!challengeContainer) {
      challengeContainer = document.createElement("div");
      challengeContainer.id = "guestbooks___challenge-answer-container";
      const websiteInput = document.querySelector("#guestbooks___guestbook-form #website").parentElement;
      websiteInput.insertAdjacentElement('afterend', challengeContainer);
    }

    challengeContainer.innerHTML = `
    <br>
    <div class="guestbooks___input-container">
        <label for="challengeQuestionAnswer">${challengeQuestion}</label> <br>
        <input placeholder="${challengeHint}" type="text" id="challengeQuestionAnswer" name="challengeQuestionAnswer" required>
    </div>
    `;
  }

  function guestbooks___loadMessages(reset) {
    // Prevent multiple simultaneous requests
    if (isLoading) {
      if (reset) reloadRequested = true;
      return;
    }

    // Don't load if we've reached the end
    if (!hasMorePages && !reset) return;

    // Reset to first page if this is a reset
    if (reset) {
      currentPage = 1;
      hasMorePages = true;
    }

    isLoading = true;

    var apiUrl =
      "{{.HostUrl}}/api/v2/get-guestbook-messages/{{.Guestbook.ID}}?page=" + currentPage + "&limit=" + pageSize;
    fetch(apiUrl)
      .then(function (response) {
        return response.json();
      })
      .then(function (data) {
        var messages = data.messages || [];
        var pagination = data.pagination || {};

        hasMorePages = pagination.hasNext || false;

        if (messages.length === 0 && currentPage === 1) {
          messagesContainer.innerHTML = "<p>There are no messages on this guestbook.</p>";
        } else {
          // Clear container only on reset (new submission or initial load)
          if (reset) {
            messagesContainer.innerHTML = "";
          }

          // Messages are already sorted by created_at DESC from the API
          messages.forEach(function (message) {
            // ignore messages that are replies (ParentMessageID not null)
            if (message.ParentMessageID) {
              return;
            }

            var messageContainer = document.createElement("div");
            messageContainer.className = "guestbook-message";

            var messageHeader = document.createElement("p");
            var boldElement = document.createElement("b");

            // add name with website (if present)
            if (message.Website) {
              var link = document.createElement("a");
              link.href = message.Website ? message.Website : "#";
              link.textContent = message.Name;
              link.target = "_blank";
              link.rel = "ugc nofollow noopener noreferrer";
              boldElement.appendChild(link);
            } else {
              var textNode = document.createTextNode(message.Name);
              boldElement.appendChild(textNode);
            }

            messageHeader.appendChild(boldElement);

            // add date
            var formattedDate = guestbooks___formatDate(message.CreatedAt);

            var dateElement = document.createElement("small");
            dateElement.textContent = " - " + formattedDate;
            messageHeader.appendChild(dateElement);

            // add actual quote
            var messageBody = document.createElement("blockquote");
            messageBody.textContent = message.Text;

            messageContainer.appendChild(messageHeader);
            messageContainer.appendChild(messageBody);

            messagesContainer.appendChild(messageContainer);

            // Add replies if they exist
            if (message.Replies && message.Replies.length > 0) {
              message.Replies.forEach(function(reply) {
                var replyContainer = document.createElement("div");
                replyContainer.className = "guestbook-message guestbook-message-reply";

                var replyHeader = document.createElement("p");
                var replyBoldElement = document.createElement("b");
                var replyTextNode = document.createTextNode(reply.Name);
                replyBoldElement.appendChild(replyTextNode);
                replyHeader.appendChild(replyBoldElement);

                // add reply date
                var replyFormattedDate = guestbooks___formatDate(reply.CreatedAt);

                var replyDateElement = document.createElement("small");
                replyDateElement.textContent = " - " + replyFormattedDate;
                replyHeader.appendChild(replyDateElement);

                // add reply text
                var replyBody = document.createElement("blockquote");
                replyBody.textContent = reply.Text;

                replyContainer.appendChild(replyHeader);
                replyContainer.appendChild(replyBody);

                messagesContainer.appendChild(replyContainer);
              });
            }
          });
        }

        // Increment page for next load
        currentPage++;
        isLoading = false;

        // Re-observe the last message for infinite scroll
        if (window.guestbooks___observeLastMessage) {
          window.guestbooks___observeLastMessage();
        }
        if (reloadRequested) {
          reloadRequested = false;
          guestbooks___loadMessages(true);
        }
      })
      .catch(function (error) {
        console.error("Error fetching messages:", error);
        isLoading = false;
        if (reloadRequested) {
          reloadRequested = false;
          guestbooks___loadMessages(true);
        }
      });
  }

  function guestbooks___setupInfiniteScroll() {
    var observer = new IntersectionObserver(function (entries) {
      entries.forEach(function (entry) {
        if (entry.isIntersecting && hasMorePages && !isLoading) {
          guestbooks___loadMessages(false); // append to existing messages
        }
      });
    }, {
      root: null, // Use the viewport as the root
      rootMargin: '200px', // Load when 200px away from the bottom
      threshold: 0.1
    });


    // Re-observe the last message whenever messages are loaded
    // Initial observation and re-observe after each load
    window.guestbooks___observeLastMessage = function () {
      var messages = messagesContainer.querySelectorAll('.guestbook-message');
      if (messages.length > 0) {
        // Stop observing previous last message
        observer.disconnect();
        // Observe the new last message
        observer.observe(messages[messages.length - 1]);
      }
    };
  }

  guestbooks___populateQuestionChallenge();
  guestbooks___loadMessages(true); // Initial load
  guestbooks___setupInfiniteScroll();

  // ---- Proof of Work Bot Deterrent ----
  {{if .Guestbook.PowEnabled}}
  (function() {
    var powChallenge = "";
    var powNonce = "";
    var powWorker = null;

    var submitBtn = submitButtons[0];

    // Build the verification UI: checkbox with inline label
    var powContainer = document.getElementById("guestbooks___pow-status");
    if (!powContainer) {
      powContainer = document.createElement("div");
      submitBtn.parentNode.insertBefore(powContainer, submitBtn);
    }
    powContainer.id = "guestbooks___pow-container";
    powContainer.className = "guestbooks___pow-container";
    powContainer.innerHTML = "";

    var powLabel = document.createElement("label");
    powLabel.className = "guestbooks___pow-checkbox-label";

    var powCheckbox = document.createElement("input");
    powCheckbox.type = "checkbox";
    powCheckbox.id = "guestbooks___pow-checkbox";

    var powLabelText = document.createElement("span");
    powLabelText.id = "guestbooks___pow-status";
    powLabelText.textContent = "I\u2019m not a robot";

    powLabel.appendChild(powCheckbox);
    powLabel.appendChild(powLabelText);
    powContainer.appendChild(powLabel);

    // Add hidden fields to carry the PoW data
    var hiddenChallenge = document.createElement("input");
    hiddenChallenge.type = "hidden";
    hiddenChallenge.name = "powChallenge";
    form.appendChild(hiddenChallenge);

    var hiddenNonce = document.createElement("input");
    hiddenNonce.type = "hidden";
    hiddenNonce.name = "powNonce";
    form.appendChild(hiddenNonce);

    // Web Worker code for SHA-256 mining using SubtleCrypto
    var workerCode = `
      self.onmessage = async function(e) {
        var challenge = e.data.challenge;
        var difficulty = e.data.difficulty;
        var batchSize = 5000;
        var nonce = 0;

        while (true) {
          for (var i = 0; i < batchSize; i++) {
            var nonceHex = nonce.toString(16);
            var input = challenge + nonceHex;
            var encoded = new TextEncoder().encode(input);
            var hashBuf = await crypto.subtle.digest("SHA-256", encoded);
            var hashArr = new Uint8Array(hashBuf);

            if (hasLeadingZeroBits(hashArr, difficulty)) {
              self.postMessage({ found: true, nonce: nonceHex, hashes: nonce + 1 });
              return;
            }
            nonce++;
          }
          self.postMessage({ found: false, hashes: nonce });
        }
      };

      function hasLeadingZeroBits(data, n) {
        var fullBytes = Math.floor(n / 8);
        var remainBits = n % 8;
        for (var i = 0; i < fullBytes; i++) {
          if (data[i] !== 0) return false;
        }
        if (remainBits > 0) {
          var mask = 0xFF << (8 - remainBits);
          if ((data[fullBytes] & mask) !== 0) return false;
        }
        return true;
      }
    `;

    function guestbooks___fetchAndSolve() {
      powReady = false;
      powChallenge = "";
      powNonce = "";
      updateSubmitState();
      powCheckbox.disabled = true;
      powLabelText.textContent = "Verifying\u2026";
      powLabelText.className = "guestbooks___pow-label-text--loading";

      var apiUrl = "{{.HostUrl}}/api/pow-challenge/{{.Guestbook.ID}}";
      fetch(apiUrl)
        .then(function(resp) { return resp.json(); })
        .then(function(data) {
          powChallenge = data.challenge;
          var difficulty = data.difficulty;

          if (powWorker) { powWorker.terminate(); }

          var blob = new Blob([workerCode], { type: "application/javascript" });
          powWorker = new Worker(URL.createObjectURL(blob));

          powWorker.onmessage = function(e) {
            if (e.data.found) {
              powNonce = e.data.nonce;
              powReady = true;
              hiddenChallenge.value = powChallenge;
              hiddenNonce.value = powNonce;
              updateSubmitState();
              powCheckbox.disabled = true;
              powLabelText.textContent = "Verified \u2713";
              powLabelText.className = "guestbooks___pow-label-text--verified";
            }
          };

          powWorker.postMessage({ challenge: powChallenge, difficulty: difficulty });
        })
        .catch(function(err) {
          console.error("PoW challenge fetch error:", err);
          powCheckbox.checked = false;
          powCheckbox.disabled = false;
          powLabelText.textContent = "Verification failed \u2014 try again";
          powLabelText.className = "guestbooks___pow-label-text--error";
        });
    }

    // Only start PoW when the checkbox is clicked
    powCheckbox.addEventListener("change", function() {
      if (powCheckbox.checked) {
        guestbooks___fetchAndSolve();
      }
    });

    resetPow = function() {
      powReady = false;
      hiddenChallenge.value = "";
      hiddenNonce.value = "";
      powCheckbox.checked = false;
      powCheckbox.disabled = false;
      powLabelText.textContent = "I\u2019m not a robot";
      powLabelText.className = "";
      updateSubmitState();
    };
  })();
  {{end}}
})();
