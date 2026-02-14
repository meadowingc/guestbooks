(function () {
  var form = document.getElementById("guestbooks___guestbook-form");
  var messagesContainer = document.getElementById(
    "guestbooks___guestbook-messages-container"
  );

  // paging state
  const pageSize = 20;
  var currentPage = 1;
  var isLoading = false;
  var hasMorePages = true;

  form.addEventListener("submit", async function (event) {
    event.preventDefault();

    var formData = new FormData(form);
    const response = await fetch(form.action, {
      method: "POST",
      body: formData,
    });

    let errorContainer = document.querySelector("#guestbooks___error-message");
    if (!errorContainer) {
      errorContainer = document.createElement("div");
      errorContainer.id = "guestbooks___error-message";
      const submitButton = document.querySelector("#guestbooks___guestbook-form input[type='submit']");
      submitButton.insertAdjacentElement('afterend', errorContainer);
    }

    if (response.ok) {
      form.reset();
      guestbooks___loadMessages(true); // clear existing messages
      errorContainer.innerHTML = "";
    } else {
      const err = await response.text();
      console.error("Error:", err);
      if (response.status === 401) {
        errorContainer.innerHTML = "{{.Guestbook.ChallengeFailedMessage}}";
      } else {
        errorContainer.innerHTML = err;
      }
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
    if (isLoading) return;

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
            var createdAt = new Date(message.CreatedAt);
            var formattedDate = createdAt.toLocaleDateString("en-US", {
              month: "short",
              day: "numeric",
              year: "numeric",
            });

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
                var replyCreatedAt = new Date(reply.CreatedAt);
                var replyFormattedDate = replyCreatedAt.toLocaleDateString("en-US", {
                  month: "short",
                  day: "numeric",
                  year: "numeric",
                });

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
      })
      .catch(function (error) {
        console.error("Error fetching messages:", error);
        isLoading = false;
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
    var powReady = false;
    var powWorker = null;

    var submitBtn = form.querySelector("input[type='submit'], button[type='submit']");
    submitBtn.disabled = true;

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
      submitBtn.disabled = true;
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
              submitBtn.disabled = false;
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

    // After form submission, reset the checkbox for the next message
    form.addEventListener("submit", function() {
      setTimeout(function() {
        powCheckbox.checked = false;
        powCheckbox.disabled = false;
        powLabelText.textContent = "I\u2019m not a robot";
        powLabelText.className = "";
        submitBtn.disabled = true;
      }, 500);
    });
  })();
  {{end}}
})();
