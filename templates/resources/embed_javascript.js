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
              link.rel = "ugc nofollow noopener";
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
})();
