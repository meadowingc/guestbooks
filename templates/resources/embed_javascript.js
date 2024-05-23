(function () {
  var form = document.getElementById("guestbooks___guestbook-form");
  var messagesContainer = document.getElementById(
    "guestbooks___guestbook-messages-container"
  );

  form.addEventListener("submit", function (event) {
    event.preventDefault();

    var formData = new FormData(form);
    fetch(form.action, {
      method: "POST",
      body: formData,
    })
      .then(function (response) {
        if (!response.ok) {
          throw new Error("Network response was not ok");
        }
      })
      .then(function () {
        form.reset();
        guestbooks___loadMessages();
      })
      .catch(function (error) {
        console.error("Error:", error);
      });
  });

  function guestbooks___loadMessages() {
    var apiUrl =
      "https://guestbooks.meadowing.club/api/v1/get-guestbook-messages/{{.GuestbookID}}";
    fetch(apiUrl)
      .then(function (response) {
        return response.json();
      })
      .then(function (messages) {
        messagesContainer.innerHTML = "";
        messages.forEach(function (message) {
          var messageContainer = document.createElement("div");

          var messageHeader = document.createElement("p");
          var boldElement = document.createElement("b");

          // add name with website (if present)
          if (message.Website) {
            var link = document.createElement("a");
            link.href = message.Website ? message.Website : "#";
            link.textContent = message.Name;
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
      })
      .catch(function (error) {
        console.error("Error fetching messages:", error);
      });
  }

  guestbooks___loadMessages();
})();
