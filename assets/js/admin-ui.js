// Admin UI Enhancements
document.addEventListener('DOMContentLoaded', function() {
    
    // Add fade-in animation to cards
    const cards = document.querySelectorAll('.card, .guestbook-card');
    cards.forEach((card, index) => {
        card.style.opacity = '0';
        card.style.transform = 'translateY(20px)';
        setTimeout(() => {
            card.style.transition = 'opacity 0.3s ease, transform 0.3s ease';
            card.style.opacity = '1';
            card.style.transform = 'translateY(0)';
        }, index * 50);
    });
    
    // Enhance form submissions with loading state
    const forms = document.querySelectorAll('form');
    forms.forEach(form => {
        form.addEventListener('submit', function(e) {
            const submitBtn = form.querySelector('button[type="submit"], input[type="submit"]');
            if (submitBtn && !submitBtn.disabled) {
                const originalText = submitBtn.innerHTML || submitBtn.value;
                const isInput = submitBtn.tagName === 'INPUT';
                
                // Add loading spinner
                if (isInput) {
                    submitBtn.value = 'Loading...';
                } else {
                    submitBtn.innerHTML = '<span class="spinner"></span> Processing...';
                }
                submitBtn.disabled = true;
                
                // Re-enable after a timeout (in case of errors)
                setTimeout(() => {
                    submitBtn.disabled = false;
                    if (isInput) {
                        submitBtn.value = originalText;
                    } else {
                        submitBtn.innerHTML = originalText;
                    }
                }, 10000);
            }
        });
    });
    
    // Add copy to clipboard functionality for embed codes
    const copyButtons = document.querySelectorAll('.copy-btn');
    copyButtons.forEach(btn => {
        btn.addEventListener('click', function() {
            const targetId = btn.getAttribute('data-target');
            const targetElement = document.getElementById(targetId);
            
            if (targetElement) {
                const text = targetElement.textContent || targetElement.value;
                navigator.clipboard.writeText(text).then(() => {
                    const originalText = btn.textContent;
                    btn.textContent = '✓ Copied!';
                    btn.classList.add('btn-success');
                    
                    setTimeout(() => {
                        btn.textContent = originalText;
                        btn.classList.remove('btn-success');
                    }, 2000);
                }).catch(err => {
                    console.error('Failed to copy:', err);
                });
            }
        });
    });
    
    // Add confirmation dialogs with better styling
    const deleteButtons = document.querySelectorAll('button[onclick*="confirm"]');
    deleteButtons.forEach(btn => {
        btn.onclick = function(e) {
            e.preventDefault();
            
            // Create custom confirmation modal
            const modal = document.createElement('div');
            modal.style.cssText = `
                position: fixed;
                top: 0;
                left: 0;
                right: 0;
                bottom: 0;
                background: rgba(0, 0, 0, 0.5);
                display: flex;
                align-items: center;
                justify-content: center;
                z-index: 10000;
                animation: fadeIn 0.2s ease;
            `;
            
            const dialog = document.createElement('div');
            dialog.style.cssText = `
                background: white;
                border-radius: var(--border-radius);
                padding: 2rem;
                max-width: 400px;
                box-shadow: var(--shadow-lg);
                animation: slideIn 0.3s ease;
            `;
            
            dialog.innerHTML = `
                <h3 style="margin-top: 0; color: var(--error-color);">⚠️ Confirm Deletion</h3>
                <p style="color: var(--gray-700);">Are you sure you want to delete this? This action cannot be undone.</p>
                <div style="display: flex; gap: 1rem; justify-content: flex-end; margin-top: 1.5rem;">
                    <button class="btn btn-outline" id="cancel-btn">Cancel</button>
                    <button class="btn btn-danger" id="confirm-btn">Delete</button>
                </div>
            `;
            
            modal.appendChild(dialog);
            document.body.appendChild(modal);
            
            // Handle modal actions
            document.getElementById('cancel-btn').onclick = () => modal.remove();
            document.getElementById('confirm-btn').onclick = () => {
                modal.remove();
                // Submit the parent form
                const form = btn.closest('form');
                if (form) {
                    form.submit();
                }
            };
            
            // Close on background click
            modal.onclick = (e) => {
                if (e.target === modal) {
                    modal.remove();
                }
            };
            
            return false;
        };
    });
    
    // Auto-save indicator for forms
    const textareas = document.querySelectorAll('textarea[name="customPageCSS"]');
    textareas.forEach(textarea => {
        let saveTimeout;
        const indicator = document.createElement('div');
        indicator.className = 'text-small text-muted';
        indicator.style.marginTop = '0.5rem';
        textarea.parentNode.appendChild(indicator);
        
        textarea.addEventListener('input', function() {
            indicator.textContent = 'Typing...';
            clearTimeout(saveTimeout);
            
            saveTimeout = setTimeout(() => {
                indicator.innerHTML = '<span style="color: var(--success-color);">✓ Ready to save</span>';
            }, 1000);
        });
    });
    
    // Enhance navigation with active state
    const currentPath = window.location.pathname;
    const navLinks = document.querySelectorAll('.nav a');
    navLinks.forEach(link => {
        if (link.getAttribute('href') === currentPath) {
            link.classList.add('active');
            link.style.background = 'var(--primary-light)';
            link.style.color = 'var(--primary-color)';
        }
    });
    
    // Add smooth scroll behavior
    document.querySelectorAll('a[href^="#"]').forEach(anchor => {
        anchor.addEventListener('click', function(e) {
            e.preventDefault();
            const target = document.querySelector(this.getAttribute('href'));
            if (target) {
                target.scrollIntoView({
                    behavior: 'smooth',
                    block: 'start'
                });
            }
        });
    });
    
    // Password strength indicator
    const passwordInputs = document.querySelectorAll('input[type="password"][name*="new"]');
    passwordInputs.forEach(input => {
        const strengthIndicator = document.createElement('div');
        strengthIndicator.className = 'form-hint';
        strengthIndicator.style.marginTop = '0.25rem';
        input.parentNode.appendChild(strengthIndicator);
        
        input.addEventListener('input', function() {
            const password = input.value;
            let strength = 0;
            let feedback = [];
            
            if (password.length >= 8) strength++;
            if (password.length >= 12) strength++;
            if (/[a-z]/.test(password) && /[A-Z]/.test(password)) strength++;
            if (/\d/.test(password)) strength++;
            if (/[^a-zA-Z\d]/.test(password)) strength++;
            
            const strengthLevels = ['Very Weak', 'Weak', 'Fair', 'Good', 'Strong'];
            const strengthColors = ['var(--error-color)', 'var(--warning-color)', '#F59E0B', '#10B981', 'var(--success-color)'];
            
            if (password.length > 0) {
                strengthIndicator.innerHTML = `
                    <span style="color: ${strengthColors[strength]}">
                        Password strength: ${strengthLevels[strength]}
                    </span>
                `;
            } else {
                strengthIndicator.innerHTML = '';
            }
        });
    });
});

// Add CSS animation keyframes dynamically
const style = document.createElement('style');
style.textContent = `
    @keyframes slideIn {
        from {
            transform: translateY(-20px);
            opacity: 0;
        }
        to {
            transform: translateY(0);
            opacity: 1;
        }
    }
`;
document.head.appendChild(style);

// Toast notification system
window.showToast = function(message, type = 'info') {
    const toast = document.createElement('div');
    toast.className = `callout callout-${type}`;
    toast.style.cssText = `
        position: fixed;
        top: 80px;
        right: 20px;
        z-index: 10000;
        max-width: 400px;
        animation: slideIn 0.3s ease;
        cursor: pointer;
    `;
    toast.innerHTML = `<p style="margin: 0;">${message}</p>`;
    
    document.body.appendChild(toast);
    
    // Auto-remove after 5 seconds
    setTimeout(() => {
        toast.style.animation = 'fadeOut 0.3s ease';
        setTimeout(() => toast.remove(), 300);
    }, 5000);
    
    // Click to dismiss
    toast.onclick = () => {
        toast.style.animation = 'fadeOut 0.3s ease';
        setTimeout(() => toast.remove(), 300);
    };
};
